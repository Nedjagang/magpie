package main

import "gopkg.in/yaml.v3"

// patchConfig silently injects known-required flags into a collector config
// before it is written to disk and applied. Operators' authored configs stay
// clean in the portal; the running config gets platform-specific workarounds
// automatically.
//
// Current patches:
//   - receivers.hostmetrics.scrapers.process: mute_process_user_error: true
//     Container runtimes (Docker distroless, Kubernetes) run processes under
//     UIDs with no /etc/passwd entry on the host (e.g. 65532). The process
//     scraper fails with "unknown userid" every cycle, blocking all metric
//     collection. This flag suppresses that error without dropping the rest
//     of the scraper output.
func patchConfig(body []byte) []byte {
	var root map[string]any
	if err := yaml.Unmarshal(body, &root); err != nil || root == nil {
		return body
	}

	receivers, _ := root["receivers"].(map[string]any)
	if receivers == nil {
		return body
	}
	hm, _ := receivers["hostmetrics"].(map[string]any)
	if hm == nil {
		return body
	}
	scrapers, _ := hm["scrapers"].(map[string]any)
	if scrapers == nil {
		return body
	}
	rawProcess, hasProcess := scrapers["process"]
	if !hasProcess {
		return body
	}

	process, _ := rawProcess.(map[string]any)
	if process == nil {
		process = map[string]any{}
	}
	if _, ok := process["mute_process_user_error"]; ok {
		return body // already set
	}

	process["mute_process_user_error"] = true
	scrapers["process"] = process
	hm["scrapers"] = scrapers
	receivers["hostmetrics"] = hm
	root["receivers"] = receivers

	patched, err := yaml.Marshal(root)
	if err != nil {
		return body
	}
	return patched
}
