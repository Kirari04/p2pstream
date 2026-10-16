//go:build !p2pstream_qualification

package server

import "net/http"

func writeQualificationCreditSnapshot(w http.ResponseWriter, _ *App, _ []int64) {
	http.Error(w, "qualification credit snapshots require the p2pstream_qualification build tag", http.StatusNotFound)
}
