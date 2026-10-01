package walg

import (
	"strconv"
	"strings"

	"github.com/israel-duff/pgdock/internal/agentapi"
)

// S3Prefix is WALG_S3_PREFIX for w: s3://<bucket>/<target prefix>/<prefix>.
func S3Prefix(w agentapi.WALG) string {
	return "s3://" + w.Storage.Bucket + "/" + strings.TrimLeft(w.Storage.Key(w.Prefix), "/")
}

// Env is the environment WAL-G needs for w (storage, credentials, key).
func Env(w agentapi.WALG) []string {
	region := w.Storage.Region
	if region == "" {
		region = "us-east-1"
	}
	env := []string{
		"WALG_S3_PREFIX=" + S3Prefix(w),
		"AWS_ACCESS_KEY_ID=" + w.Storage.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + w.Storage.SecretKey,
		"AWS_REGION=" + region,
		"AWS_S3_FORCE_PATH_STYLE=" + strconv.FormatBool(w.Storage.PathStyle),
		"WALG_PGP_KEY=" + w.PGPKey,
		"WALG_COMPRESSION_METHOD=zstd",
	}
	if w.Storage.Endpoint != "" {
		env = append(env, "AWS_ENDPOINT="+w.Storage.Endpoint)
	}
	return env
}
