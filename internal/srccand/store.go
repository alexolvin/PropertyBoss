package srccand

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Candidate struct {
	Domain         string
	DiscoveredVia  string
	Class          string
	ClassReason    *string
	EvidencePath   *string
	ProbeRunIDs    []string
	ProbedAt       *time.Time
	Channel        *string
	RobotsOK       *bool
	ReportedTotals []byte
	MirrorOf       *string
	TosURL         *string
	TosExcerpt     *string
	FieldSample    []byte
	DelistSig      []byte
}

func NormalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "www.")
	if i := strings.IndexByte(d, '/'); i >= 0 {
		d = d[:i]
	}
	return d
}

func validVia(via string) bool {
	if via == "seed" || via == "owner" {
		return true
	}
	return strings.HasPrefix(via, "link:") && len(via) > 5
}

func Add(ctx context.Context, pool *pgxpool.Pool, domain, via string) (inserted bool, err error) {
	domain = NormalizeDomain(domain)
	if domain == "" || !strings.Contains(domain, ".") {
		return false, fmt.Errorf("sources: некорректный домен %q", domain)
	}
	if !validVia(via) {
		return false, fmt.Errorf("sources: --via seed|owner|link:<домен>, задано %q", via)
	}
	class := "pending"
	if domain == "bazos.cz" || domain == "reality.bazos.cz" {
		class = "already_connected"
	}
	tag, err := pool.Exec(ctx, `
INSERT INTO source_candidates (domain, discovered_via, class)
VALUES ($1, $2, $3)
ON CONFLICT (domain) DO NOTHING`, domain, via, class)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func Get(ctx context.Context, pool *pgxpool.Pool, domain string) (*Candidate, error) {
	domain = NormalizeDomain(domain)
	row := pool.QueryRow(ctx, `
SELECT domain, discovered_via, class, class_reason, evidence_path, probe_run_ids,
       probed_at, channel, robots_ok, reported_totals, mirror_of, tos_url, tos_excerpt,
       field_sample, delist_signature
FROM source_candidates WHERE domain=$1`, domain)
	var c Candidate
	err := row.Scan(&c.Domain, &c.DiscoveredVia, &c.Class, &c.ClassReason, &c.EvidencePath,
		&c.ProbeRunIDs, &c.ProbedAt, &c.Channel, &c.RobotsOK, &c.ReportedTotals, &c.MirrorOf,
		&c.TosURL, &c.TosExcerpt, &c.FieldSample, &c.DelistSig)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func List(ctx context.Context, pool *pgxpool.Pool) ([]Candidate, error) {
	rows, err := pool.Query(ctx, `
SELECT domain, discovered_via, class, class_reason, evidence_path, probe_run_ids,
       probed_at, channel, robots_ok, reported_totals, mirror_of, tos_url, tos_excerpt,
       field_sample, delist_signature
FROM source_candidates ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Domain, &c.DiscoveredVia, &c.Class, &c.ClassReason, &c.EvidencePath,
			&c.ProbeRunIDs, &c.ProbedAt, &c.Channel, &c.RobotsOK, &c.ReportedTotals, &c.MirrorOf,
			&c.TosURL, &c.TosExcerpt, &c.FieldSample, &c.DelistSig); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func SaveProbe(ctx context.Context, pool *pgxpool.Pool, domain, class, reason, evidence, runID string, robotsOK *bool, channel, tosURL, tosExcerpt *string) error {
	return SaveUpdate(ctx, pool, Update{
		Domain: domain, Class: class, Reason: reason, Evidence: evidence, RunID: runID,
		RobotsOK: robotsOK, Channel: channel, TosURL: tosURL, TosExcerpt: tosExcerpt,
	})
}

// Update — запись итога пробы.
type Update struct {
	Domain, Class, Reason, Evidence, RunID string
	RobotsOK                               *bool
	Channel, TosURL, TosExcerpt, MirrorOf  *string
	ReportedTotals, FieldSample, DelistSig []byte
}

func SaveUpdate(ctx context.Context, pool *pgxpool.Pool, u Update) error {
	u.Domain = NormalizeDomain(u.Domain)
	_, err := pool.Exec(ctx, `
UPDATE source_candidates SET
    class = $2,
    class_reason = $3,
    evidence_path = $4,
    probe_run_ids = array_append(probe_run_ids, $5),
    probed_at = now(),
    robots_ok = $6,
    channel = $7,
    tos_url = $8,
    tos_excerpt = $9,
    mirror_of = COALESCE($10, mirror_of),
    reported_totals = COALESCE($11::jsonb, reported_totals),
    field_sample = COALESCE($12::jsonb, field_sample),
    delist_signature = COALESCE($13::jsonb, delist_signature)
WHERE domain = $1`,
		u.Domain, u.Class, u.Reason, u.Evidence, u.RunID,
		u.RobotsOK, u.Channel, u.TosURL, u.TosExcerpt, u.MirrorOf,
		nullJSON(u.ReportedTotals), nullJSON(u.FieldSample), nullJSON(u.DelistSig))
	return err
}

func nullJSON(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func Pending(ctx context.Context, pool *pgxpool.Pool) ([]Candidate, error) {
	return Due(ctx, pool, 0)
}

// Due — pending, которых ещё не пробировали, либо прошло ≥ minAge с probed_at
// (второй прогон dead/captcha/challenge/blocked — не раньше 1 ч).
func Due(ctx context.Context, pool *pgxpool.Pool, minAge time.Duration) ([]Candidate, error) {
	all, err := List(ctx, pool)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []Candidate
	var first *Candidate
	for i := range all {
		c := all[i]
		if c.Class != "pending" {
			continue
		}
		if c.ProbedAt != nil && minAge > 0 && now.Sub(*c.ProbedAt) < minAge {
			continue
		}
		if c.Domain == "bezrealitky.cz" {
			cc := c
			first = &cc
			continue
		}
		out = append(out, c)
	}
	if first != nil {
		return append([]Candidate{*first}, out...), nil
	}
	return out, nil
}
