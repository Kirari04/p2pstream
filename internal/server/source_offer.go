// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"fmt"
	"net/http"
	"strings"

	"p2pstream/internal/buildinfo"
)

const sourceOfferPath = "/.well-known/p2pstream/source"

func sourceOfferHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write([]byte(sourceOfferText()))
}

func sourceOfferText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "p2pstream\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "License: %s\n", buildinfo.License)
	fmt.Fprintf(&b, "Copyright: Copyright (C) 2026 p2pstream contributors\n")
	fmt.Fprintf(&b, "Repository: %s\n", buildinfo.RepositoryURL())
	fmt.Fprintf(&b, "Source reference: %s\n", buildinfo.SourceReference())
	fmt.Fprintf(&b, "Corresponding source: %s\n", buildinfo.SourceURL())
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "p2pstream is free software: you can redistribute it and/or modify it under the terms of the GNU Affero General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version.\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "p2pstream is distributed without warranty, including without the implied warranty of merchantability or fitness for a particular purpose. See the GNU Affero General Public License for more details.\n")
	return b.String()
}
