package faildomain

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Groups whose members must be in different failure domains (V3.1 §2.2).
const (
	GroupHAPair     = "ha_pair"
	GroupEtcd       = "etcd"
	GroupPoolerPair = "pooler_pair"
)

// Problem is a group whose members share a failure domain.
type Problem struct {
	Group     string
	Region    string
	Key       string // the group's identity, stable for alerts
	ProjectID *uuid.UUID
	Nodes     []string
	Detail    string
}

// Check lists the HA pairs, etcd members and pooler pairs that break the
// rule (V3.1 §2.4): after an upgrade, or after an admin changed a domain.
func Check(ctx context.Context, q *store.Queries) ([]Problem, error) {
	all, err := q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]store.Node{}
	for _, n := range all {
		byID[n.ID] = n
	}
	var out []Problem
	add := func(group, region, key string, project *uuid.UUID, ns []store.Node, what string) {
		if ok, pair := AllSeparated(ns); !ok {
			out = append(out, Problem{Group: group, Region: region, Key: key, ProjectID: project,
				Nodes: []string{pair[0].Name, pair[1].Name}, Detail: what + " share a failure domain: " + Describe(ns)})
		}
	}

	// HA pairs: the members of each instance with HA on.
	insts, err := q.ListPatroniInstances(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range insts {
		if !inst.HaEnabled {
			continue
		}
		members, err := q.ListInstanceMembers(ctx, inst.ID)
		if err != nil {
			return nil, err
		}
		var ns []store.Node
		region := ""
		for _, m := range members {
			if n, ok := byID[m.NodeID]; ok {
				ns = append(ns, n)
				region = n.Region
			}
		}
		var project *uuid.UUID
		name := inst.ID.String()
		if p, err := q.ProjectOnInstance(ctx, inst.ID); err == nil {
			project, name = &p.ID, p.Name
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		add(GroupHAPair, region, "ha:"+inst.ID.String(), project, ns, "the primary and standby of "+name)
	}

	// Each region's etcd members.
	members, err := q.ListEtcdMembers(ctx)
	if err != nil {
		return nil, err
	}
	etcd := map[string][]store.Node{}
	var etcdRegions []string
	for _, m := range members {
		if n, ok := byID[m.NodeID]; ok {
			if _, seen := etcd[m.Region]; !seen {
				etcdRegions = append(etcdRegions, m.Region)
			}
			etcd[m.Region] = append(etcd[m.Region], n)
		}
	}
	for _, r := range etcdRegions {
		key := "etcd:" + r
		add(GroupEtcd, r, key, nil, etcd[r], r+"'s etcd members")
	}

	// Each region's pooler hosts.
	hosts, err := q.PoolerHosts(ctx)
	if err != nil {
		return nil, err
	}
	byRegion := map[string][]store.Node{}
	var regions []string
	for _, h := range hosts {
		if _, seen := byRegion[h.Region]; !seen {
			regions = append(regions, h.Region)
		}
		byRegion[h.Region] = append(byRegion[h.Region], h)
	}
	for _, r := range regions {
		add(GroupPoolerPair, r, "pooler:"+r, nil, byRegion[r], "the pooler hosts in "+r)
	}
	return out, nil
}
