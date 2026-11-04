package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/probe"
	"propertyboss/internal/srccand"
)

func runProbe(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	pending := fs.Bool("pending", false, "все кандидаты class=pending (bezrealitky.cz первым)")
	reprobe := fs.Bool("reprobe", false, "включить pending с probed_at < 1 ч")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	var domains []string
	if *pending {
		age := time.Hour
		if *reprobe {
			age = 0
		}
		list, err := srccand.Due(ctx, pool, age)
		if err != nil {
			return err
		}
		for _, c := range list {
			domains = append(domains, c.Domain)
		}
	} else {
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: pb probe <domain> | pb probe --pending")
		}
		domains = fs.Args()
	}
	if len(domains) == 0 {
		log.Printf("probe: нечего пробировать")
		return nil
	}
	host, _ := os.Hostname()
	ua := cfg.Source.UserAgent
	for _, d := range domains {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := probeOne(ctx, pool, ua, host, d); err != nil {
			return err
		}
	}
	return nil
}

func probeOne(ctx context.Context, pool *pgxpool.Pool, ua, host, domain string) error {
	cur, err := srccand.Get(ctx, pool, domain)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("probe: %s нет в source_candidates (сначала pb sources add-candidate)", domain)
	}
	var prev *probe.Prev
	if cur.ProbedAt != nil {
		p := &probe.Prev{ProbedAt: *cur.ProbedAt, Class: cur.Class}
		if cur.ClassReason != nil {
			p.Reason = *cur.ClassReason
		}
		prev = p
	}
	log.Printf("probe: start %s class=%s ua=%q host=%s", cur.Domain, cur.Class, ua, host)
	res, err := probe.Run(ctx, probe.Config{
		Domain: cur.Domain,
		UA:     ua,
		Root:   "artifacts/probes",
		Prev:   prev,
	})
	if err != nil {
		return err
	}
	err = srccand.SaveUpdate(ctx, pool, srccand.Update{
		Domain:         res.Domain,
		Class:          res.Class,
		Reason:         res.ClassReason,
		Evidence:       res.EvidencePath,
		RunID:          res.RunID,
		RobotsOK:       res.RobotsOK,
		Channel:        res.Channel,
		TosURL:         res.TosURL,
		TosExcerpt:     res.TosExcerpt,
		MirrorOf:       res.MirrorOf,
		ReportedTotals: res.ReportedTotals,
		FieldSample:    res.FieldSample,
		DelistSig:      res.DelistSig,
	})
	if err != nil {
		return err
	}
	nAdd := 0
	for _, h := range res.Outbound {
		ins, err := srccand.Add(ctx, pool, h, "link:"+res.Domain)
		if err != nil {
			return err
		}
		if ins {
			nAdd++
			log.Printf("probe: D1 +%s via link:%s", h, res.Domain)
		}
	}
	log.Printf("probe: done %s class=%s observed=%s requests=%d run_id=%s evidence=%s d1_new=%d",
		res.Domain, res.Class, res.ObservedClass, res.Requests, res.RunID, res.EvidencePath, nAdd)
	return nil
}
