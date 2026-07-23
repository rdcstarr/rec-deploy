package notify

import "log/slog"

// toJournald logs the summary. Under systemd the daemon's stderr is journald,
// so a structured slog line is the journald channel — no dependency, and
// `journalctl -u rec-deploy` shows it.
func toJournald(s Summary, body string) {
	log := slog.Info
	if s.Status != "success" {
		log = slog.Error
	}

	log("deploy",
		"repository", s.Repository,
		"ref", s.Ref,
		"sha", s.SHA,
		"status", s.Status,
		"paths", len(s.Paths),
		"summary", body,
	)
}
