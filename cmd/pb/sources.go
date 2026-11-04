package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/probe"
	"propertyboss/internal/srccand"
)

func runSources(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("sources: субкоманда add-candidate | report | harvest")
	}
	switch args[0] {
	case "add-candidate":
		return runAddCandidate(ctx, cfg, args[1:])
	case "report":
		return runSourcesReport(ctx, cfg, args[1:])
	case "harvest":
		return runHarvest(ctx, cfg, args[1:])
	case "finalize":
		return runFinalize(ctx, cfg)
	default:
		return fmt.Errorf("sources: неизвестная субкоманда %q", args[0])
	}
}

func runAddCandidate(ctx context.Context, cfg *config.Config, args []string) error {
	via, domains := pullFlag(args, "via")
	if via == "" || len(domains) < 1 {
		return fmt.Errorf("usage: pb sources add-candidate <domain> --via seed|owner|link:<домен>")
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, d := range domains {
		ins, err := srccand.Add(ctx, pool, d, via)
		if err != nil {
			return err
		}
		if ins {
			log.Printf("sources: добавлен %s via=%s", srccand.NormalizeDomain(d), via)
		} else {
			log.Printf("sources: уже есть %s", srccand.NormalizeDomain(d))
		}
	}
	return nil
}

func runSourcesReport(ctx context.Context, cfg *config.Config, _ []string) error {
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	list, err := srccand.List(ctx, pool)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	commit := gitHead()
	runID := time.Now().UTC().Format("20060102T150405Z")
	path := "docs/sources/CZ_REGISTRY.md"
	if err := srccand.WriteRegistry(path, commit, runID, list); err != nil {
		return err
	}
	log.Printf("sources report: %s кандидатов=%d run_id=%s host=%s", path, len(list), runID, host)
	return nil
}

func runHarvest(ctx context.Context, cfg *config.Config, args []string) error {
	via, rest := pullFlag(args, "via")
	rawURL, rest := pullFlag(rest, "url")
	_ = rest
	if via == "" || rawURL == "" {
		return fmt.Errorf("usage: pb sources harvest --url URL --via link:<домен>")
	}
	ua := cfg.Source.UserAgent
	cli := probe.NewClient(ua, 30*time.Second)
	body, rec, kind, err := cli.Get(ctx, rawURL)
	if err != nil {
		return fmt.Errorf("harvest: %s: %w", rec.URL, err)
	}
	if kind != probe.KindNone {
		return fmt.Errorf("harvest: защита %s status=%d", kind, rec.Status)
	}
	from := srccand.NormalizeDomain(rawURL)
	if strings.HasPrefix(via, "link:") {
		from = strings.TrimPrefix(via, "link:")
	}
	hosts := probe.HarvestAll(string(body), from)
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	n := 0
	for _, h := range hosts {
		ins, err := srccand.Add(ctx, pool, h, via)
		if err != nil {
			return err
		}
		if ins {
			n++
			log.Printf("sources: harvest +%s", h)
		}
	}
	log.Printf("sources harvest: url=%s status=%d новых=%d всего_на_странице=%d", rec.FinalURL, rec.Status, n, len(hosts))
	return nil
}

func runFinalize(ctx context.Context, cfg *config.Config) error {
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	n, err := srccand.FinalizePending(ctx, pool, "artifacts/probes")
	if err != nil {
		return err
	}
	log.Printf("sources finalize: переклассифицировано %d pending", n)
	return runSourcesReport(ctx, cfg, nil)
}

func pullFlag(args []string, name string) (val string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--"+name && i+1 < len(args):
			val = args[i+1]
			i++
		case strings.HasPrefix(a, "--"+name+"="):
			val = strings.TrimPrefix(a, "--"+name+"=")
		default:
			rest = append(rest, a)
		}
	}
	return val, rest
}

func gitHead() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
