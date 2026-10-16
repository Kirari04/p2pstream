//go:build linux && !p2pstream_qualification

package agent

import "net/http"

func writeExternalQualificationCreditSnapshot(w http.ResponseWriter, _ *externalQualificationSessionRegistry) {
	http.Error(w, "qualification credit snapshots require the p2pstream_qualification build tag", http.StatusNotFound)
}

func writeExternalQualificationGrowthSnapshot(w http.ResponseWriter, _ *externalQualificationSessionRegistry) {
	http.Error(w, "qualification growth snapshots require the p2pstream_qualification build tag", http.StatusNotFound)
}
