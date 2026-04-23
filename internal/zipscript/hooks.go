package zipscript

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
)

type CompleteHookContext struct {
	DirPath      string
	RelName      string
	ReleaseName  string
	ReleaseSubdir string
	Section      string
	SectionRoot  string
	TotalBytes   int64
	TotalFiles   int
	DurationMs   int64
	Duration     string
	AvgSpeedMB   float64
	UserCount    int
	Data         map[string]string
}

func RunOnCompleteHook(cfg Config, ctx CompleteHookContext) {
	hook := cfg.Hooks.OnComplete
	if !cfg.Enabled || !hook.Enabled || strings.TrimSpace(hook.Command) == "" {
		return
	}

	timeout := hook.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	execCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, hook.Command, hook.Args...)
	cmd.Env = append(os.Environ(), buildCompleteHookEnv(hook.ExtraEnv, ctx)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[ZIPSCRIPT] on_complete hook failed: %v", err)
		if len(out) > 0 {
			log.Printf("[ZIPSCRIPT] on_complete output: %s", strings.TrimSpace(string(out)))
		}
		return
	}
	if cfg.Debug && len(out) > 0 {
		log.Printf("[ZIPSCRIPT] on_complete output: %s", strings.TrimSpace(string(out)))
	}
}

func buildCompleteHookEnv(extra map[string]string, ctx CompleteHookContext) []string {
	env := map[string]string{
		"GOFTPD_EVENT":          "COMPLETE",
		"GOFTPD_DIR":            strings.TrimSpace(ctx.DirPath),
		"GOFTPD_PATH":           strings.TrimSpace(ctx.DirPath),
		"GOFTPD_RELNAME":        strings.TrimSpace(ctx.RelName),
		"GOFTPD_RELEASE_NAME":   strings.TrimSpace(ctx.ReleaseName),
		"GOFTPD_RELEASE_SUBDIR": strings.TrimSpace(ctx.ReleaseSubdir),
		"GOFTPD_SECTION":        strings.TrimSpace(ctx.Section),
		"GOFTPD_SECTION_ROOT":   strings.TrimSpace(ctx.SectionRoot),
		"GOFTPD_TOTAL_BYTES":    fmt.Sprintf("%d", ctx.TotalBytes),
		"GOFTPD_TOTAL_MB":       fmt.Sprintf("%.2f", float64(ctx.TotalBytes)/1024.0/1024.0),
		"GOFTPD_TOTAL_FILES":    fmt.Sprintf("%d", ctx.TotalFiles),
		"GOFTPD_DURATION_MS":    fmt.Sprintf("%d", ctx.DurationMs),
		"GOFTPD_DURATION":       strings.TrimSpace(ctx.Duration),
		"GOFTPD_AVG_SPEED_MB":   fmt.Sprintf("%.2f", ctx.AvgSpeedMB),
		"GOFTPD_USER_COUNT":     fmt.Sprintf("%d", ctx.UserCount),
	}
	for k, v := range ctx.Data {
		env["GOFTPD_"+sanitizeEnvKey(k)] = v
	}
	for k, v := range extra {
		env[strings.TrimSpace(k)] = v
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func sanitizeEnvKey(key string) string {
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		return "VALUE"
	}
	var b strings.Builder
	for _, r := range key {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func SectionInfoFromPath(dirPath string) (section string, root string) {
	cleaned := path.Clean("/" + strings.TrimSpace(dirPath))
	if cleaned == "/" || cleaned == "." {
		return "DEFAULT", "DEFAULT"
	}
	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "DEFAULT", "DEFAULT"
	}
	root = strings.ToUpper(parts[0])
	section = root
	if len(parts) >= 2 {
		switch root {
		case "FOREIGN", "PRE", "ARCHIVE":
			section = strings.ToUpper(parts[1])
		}
	}
	return section, root
}
