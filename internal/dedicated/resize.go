package dedicated

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Resizing a dedicated instance and growing its disk (V4.1 §5). The new
// size is written onto the instance and its containers are recreated with
// it: a restart of a few seconds under the poolers' pause without HA, a
// rolling restart with a switchover with HA. Read replicas take the
// instance's size, so they follow. A size the node can't hold becomes a
// node move into an instance of the new size.

// KindResize is the resize operation.
const KindResize = "resize_instance"

// Size is a dedicated instance's size.
type Size struct {
	CPUs     float64 `json:"cpus"`
	MemoryMB int     `json:"memory_mb"`
	DiskGB   int     `json:"disk_gb"`
}

// SizeOf is inst's size.
func SizeOf(inst store.Instance) Size {
	p := profileOf(inst)
	sz := Size{CPUs: p.CPUs, MemoryMB: p.MemoryMB, DiskGB: DefaultVolumeGB}
	if inst.VolumeGb != nil {
		sz.DiskGB = int(*inst.VolumeGb)
	}
	return sz
}

// profileName is the profile a size matches, or "custom".
func profileName(sz Size) string {
	for _, p := range Profiles {
		if p.CPUs == sz.CPUs && p.MemoryMB == sz.MemoryMB {
			return p.Name
		}
	}
	return "custom"
}

// ResizeParams asks for a new size; zero fields keep the current value.
type ResizeParams struct {
	ProjectID uuid.UUID
	Size      Size
	CreatedBy *uuid.UUID
}

// ResizePlan is what a resize will do.
type ResizePlan struct {
	From, To Size
	// Move is the node the project moves to when its own can't hold the
	// new size; nil when it is resized in place.
	Move     *uuid.UUID
	MoveName string
	// Restart: the containers are recreated (a CPU or memory change); a
	// disk change alone needs no restart.
	Restart bool
}

type resizeParams struct {
	From Size `json:"from"`
	To   Size `json:"to"`
}

// ErrNoRoom is a resize no node can hold.
var ErrNoRoom = errors.New("no node has room")

// room is what a node has left for dedicated instances, from its agent's
// last report; known is false before it reported.
func room(n store.CapacityNodesRow) (cpus float64, memMB, diskGB int64, known bool) {
	var m agentapi.HostMetrics
	_ = json.Unmarshal(n.Capacity, &m)
	if m.CPUs <= 0 || m.MemTotalBytes <= 0 {
		return 0, 0, 0, false
	}
	return float64(m.CPUs) - n.DedicatedCpus, m.MemTotalBytes>>20 - n.ReservedMemMb, m.DiskTotalBytes>>30 - n.DedicatedDiskGb, true
}

// fits reports whether n can take need more (a node that hasn't reported
// its size is taken at its word), and if not, why.
func fits(n store.CapacityNodesRow, cpus float64, memMB, diskGB int64) (bool, string) {
	c, m, d, known := room(n)
	if !known {
		return true, ""
	}
	switch {
	case cpus > c+1e-9:
		return false, fmt.Sprintf("node %s has %.2f vCPU free, %.2f more needed", n.Name, math.Max(c, 0), cpus)
	case memMB > m:
		return false, fmt.Sprintf("node %s has %d MB of memory free, %d more needed", n.Name, max(m, 0), memMB)
	case diskGB > 0 && diskGB > d:
		return false, fmt.Sprintf("node %s has %d GB of disk unallocated, %d more needed", n.Name, max(d, 0), diskGB)
	}
	return true, ""
}

// PlanResize checks a resize: the sizes, and room on the instance's nodes
// (or, failing that, another node in its region to move to).
func (s *Service) PlanResize(ctx context.Context, p store.Project, want Size) (ResizePlan, error) {
	if p.Tier != provision.TierDedicated {
		return ResizePlan{}, fmt.Errorf("%w: only dedicated projects are resized; promote this one first", provision.ErrInvalid)
	}
	q := store.New(s.db)
	inst, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		return ResizePlan{}, err
	}
	from := SizeOf(inst)
	to := from
	if want.CPUs != 0 {
		to.CPUs = want.CPUs
	}
	if want.MemoryMB != 0 {
		to.MemoryMB = want.MemoryMB
	}
	if want.DiskGB != 0 {
		to.DiskGB = want.DiskGB
	}
	switch {
	case to.CPUs < 0.25 || to.CPUs > 64:
		return ResizePlan{}, fmt.Errorf("%w: cpus must be 0.25 to 64", provision.ErrInvalid)
	case to.MemoryMB < 512 || to.MemoryMB > 262144:
		return ResizePlan{}, fmt.Errorf("%w: memory_mb must be 512 to 262144", provision.ErrInvalid)
	case to.DiskGB < from.DiskGB:
		return ResizePlan{}, fmt.Errorf("%w: a disk only grows; to shrink it, move the project into a smaller instance (demote and promote again, or a node move)", provision.ErrInvalid)
	case to.DiskGB > 16384:
		return ResizePlan{}, fmt.Errorf("%w: disk_gb must be at most 16384", provision.ErrInvalid)
	case to == from:
		return ResizePlan{}, fmt.Errorf("%w: that is the instance's size already", provision.ErrInvalid)
	}
	plan := ResizePlan{From: from, To: to, Restart: to.CPUs != from.CPUs || to.MemoryMB != from.MemoryMB}
	nodes, err := q.CapacityNodes(ctx)
	if err != nil {
		return plan, err
	}
	byID := map[uuid.UUID]store.CapacityNodesRow{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	dCPU, dMem, dDisk := to.CPUs-from.CPUs, int64(to.MemoryMB-from.MemoryMB), int64(to.DiskGB-from.DiskGB)
	// The instance's nodes: its own, and with HA or replicas each member's.
	hosts := []uuid.UUID{inst.NodeID}
	if inst.Patroni {
		members, err := q.ListInstanceMembers(ctx, inst.ID)
		if err != nil {
			return plan, err
		}
		for _, m := range members {
			if m.NodeID != inst.NodeID {
				hosts = append(hosts, m.NodeID)
			}
		}
	}
	var why []string
	for _, h := range hosts {
		n, ok := byID[h]
		if !ok {
			continue
		}
		if ok, reason := fits(n, dCPU, dMem, dDisk); !ok {
			why = append(why, reason)
		}
	}
	if len(why) == 0 {
		return plan, nil
	}
	// It doesn't fit where it is: a node move into an instance of the new
	// size, when the project can move (V3 §2.3).
	if inst.HaEnabled || len(hosts) > 1 {
		return plan, fmt.Errorf("%w: %s; with HA or read replicas the members can't move, so ask the platform admin for room (or a resize that fits)",
			ErrNoRoom, why[0])
	}
	for _, n := range nodes {
		if n.ID == inst.NodeID || n.Region != p.Region || (n.Role != "dedicated" && n.Role != "both") ||
			n.Status != "healthy" || n.Lifecycle != "active" || n.AgentCertFp == nil {
			continue
		}
		if ok, _ := fits(n, to.CPUs, int64(to.MemoryMB), int64(to.DiskGB)); ok {
			id := n.ID
			plan.Move, plan.MoveName = &id, n.Name
			return plan, nil
		}
	}
	return plan, fmt.Errorf("%w: %s, and no other node in %s has room for %g vCPU and %d MB", ErrNoRoom, why[0], p.Region, to.CPUs, to.MemoryMB)
}

// Resize queues a resize, or the node move it needs.
func (s *Service) Resize(ctx context.Context, rp ResizeParams) (store.Operation, ResizePlan, error) {
	p, err := store.New(s.db).GetProject(ctx, rp.ProjectID)
	if err != nil {
		return store.Operation{}, ResizePlan{}, err
	}
	plan, err := s.PlanResize(ctx, p, rp.Size)
	if err != nil {
		return store.Operation{}, plan, err
	}
	if plan.Move != nil {
		to := plan.To
		op, err := s.Move(ctx, MoveParams{ProjectID: rp.ProjectID, NodeID: *plan.Move, CreatedBy: rp.CreatedBy, Size: &to})
		return op, plan, err
	}
	op, err := s.projects.EnqueueExclusive(ctx, rp.ProjectID, []string{provision.StatusActive}, "", KindResize,
		resizeParams{From: plan.From, To: plan.To}, rp.CreatedBy, nil)
	return op, plan, err
}

func opResize(op store.Operation) (resizeParams, error) {
	var p resizeParams
	if err := json.Unmarshal(op.Params, &p); err != nil || p.To.CPUs == 0 {
		return p, jobs.Permanent(fmt.Errorf("resize params: %w", err))
	}
	return p, nil
}

// setSize writes sz onto the instance.
func (s *Service) setSize(ctx context.Context, id uuid.UUID, sz Size) error {
	mem, vol, name := int32(sz.MemoryMB), int32(sz.DiskGB), profileName(sz)
	return store.New(s.db).SetInstanceSize(ctx, store.SetInstanceSizeParams{ID: id, CpuLimit: numeric(sz.CPUs), MemLimitMb: &mem, VolumeGb: &vol, Profile: &name})
}

// runResize recreates the instance's containers at the new size.
func (s *Service) runResize(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	rp, err := opResize(op)
	if err != nil {
		return err
	}
	q := store.New(s.db)
	p, err := q.GetProject(ctx, *op.ProjectID)
	if err != nil {
		return jobs.Permanent(err)
	}
	if err := s.setSize(ctx, p.InstanceID, rp.To); err != nil {
		return err
	}
	inst, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		return err
	}
	if err := log.Info(ctx, "resize", "%g → %g vCPU, %d → %d MB, disk %d → %d GB", rp.From.CPUs, rp.To.CPUs,
		rp.From.MemoryMB, rp.To.MemoryMB, rp.From.DiskGB, rp.To.DiskGB); err != nil {
		return err
	}
	if rp.To.CPUs != rp.From.CPUs || rp.To.MemoryMB != rp.From.MemoryMB {
		var pause time.Duration
		if inst.HaEnabled {
			// Standbys and replicas first, a switchover, then the old primary.
			_, ms, err := s.minorUpgradeHA(ctx, inst, p)
			if err != nil {
				return err
			}
			if ms != nil {
				pause = time.Duration(*ms) * time.Millisecond
			}
			if err := log.Info(ctx, "restart", "standby resized, switched over, old primary resized"); err != nil {
				return err
			}
		} else {
			dbs := store.PoolerNames(p)
			start := time.Now()
			if _, err := s.projects.Pooler().Freeze(ctx, freezeWait, dbs...); err != nil {
				_ = s.projects.Pooler().Resume(context.WithoutCancel(ctx), dbs...)
				return fmt.Errorf("pause the poolers: %w", err)
			}
			_, rerr := s.Recreate(ctx, inst)
			if err := s.projects.Pooler().Resume(context.WithoutCancel(ctx), dbs...); err != nil && !isNotPaused(err) {
				rerr = errors.Join(rerr, fmt.Errorf("pooler RESUME: %w", err))
			}
			pause = time.Since(start)
			if rerr != nil {
				return rerr
			}
			if err := log.Info(ctx, "restart", "recreated with the new limits"); err != nil {
				return err
			}
			if inst.Patroni {
				// Read replicas, one at a time, each out of rotation meanwhile.
				if err := s.recreateReplicas(ctx, inst); err != nil {
					return err
				}
			}
		}
		if err := log.Info(ctx, "pause", "writes paused for %s", pause.Round(time.Millisecond)); err != nil {
			return err
		}
	}
	if rp.To.DiskGB != rp.From.DiskGB {
		if err := s.followDiskWarn(ctx, p.ID, rp.From.DiskGB, rp.To.DiskGB); err != nil {
			return err
		}
	}
	return log.Info(ctx, "done", "instance resized")
}

// failResize puts the old size back on the instance (its containers are
// recreated at it next time they are, which the retry or the admin does).
func (s *Service) failResize(ctx context.Context, op store.Operation, log *jobs.StepLogger, cause error) error {
	rp, err := opResize(op)
	if err != nil || op.ProjectID == nil {
		return cause
	}
	p, err := store.New(s.db).GetProject(ctx, *op.ProjectID)
	if err != nil {
		return cause
	}
	if err := s.setSize(ctx, p.InstanceID, rp.From); err != nil {
		return errors.Join(cause, err)
	}
	inst, err := store.New(s.db).GetInstance(ctx, p.InstanceID)
	if err == nil && !inst.HaEnabled && (rp.To.CPUs != rp.From.CPUs || rp.To.MemoryMB != rp.From.MemoryMB) {
		if _, err := s.Recreate(ctx, inst); err != nil {
			_ = log.Warn(ctx, "rollback", "recreating at the old size failed: %v", err)
		}
	}
	_ = log.Warn(ctx, "rollback", "the instance keeps its old size")
	return cause
}

// followDiskWarn moves the project's disk warning with its volume when it
// is still the default (80% of the volume).
func (s *Service) followDiskWarn(ctx context.Context, projectID uuid.UUID, fromGB, toGB int) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		p, err := q.GetLiveProjectForUpdate(ctx, projectID)
		if err != nil {
			return err
		}
		set, err := store.DecodeProjectSettings(p.Settings)
		if err != nil {
			return err
		}
		if set.DiskWarnBytes != DefaultDiskWarn(fromGB) {
			return nil // the owner chose their own
		}
		set.DiskWarnBytes = DefaultDiskWarn(toGB)
		raw, err := json.Marshal(set)
		if err != nil {
			return err
		}
		_, err = q.UpdateProjectMeta(ctx, store.UpdateProjectMetaParams{ID: p.ID, Name: p.Name, Description: p.Description, Settings: raw})
		return err
	})
}

// DefaultDiskWarn is the disk warning for a volume: 80% of it.
func DefaultDiskWarn(volumeGB int) int64 {
	return store.DefaultDedicatedSettings(volumeGB).DiskWarnBytes
}
