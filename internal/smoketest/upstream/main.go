package main

import (
	"bufio"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var retryAsset = &retryAssetState{attemptsByPath: make(map[string]int)}
var retryStatus = &retryStatusState{attemptsByPath: make(map[string]int)}

type retryAssetState struct {
	mu             sync.Mutex
	totalAttempts  int
	attemptsByPath map[string]int
}

type retryStatusState struct {
	mu             sync.Mutex
	attemptsByPath map[string]int
}

func main() {
	addr := strings.TrimSpace(os.Getenv("SMOKE_UPSTREAM_ADDR"))
	if addr == "" {
		addr = ":9000"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	mux.HandleFunc("/headers", headersHandler)
	mux.HandleFunc("/echo", echoHandler)
	mux.HandleFunc("/stream", streamHandler)
	mux.HandleFunc("/slow-headers", slowHeadersHandler)
	mux.HandleFunc("/close-early", closeEarlyHandler)
	mux.HandleFunc("/retry-assets/", retryAssetHandler)
	mux.HandleFunc("/retry-asset-status", retryAssetStatusHandler)
	mux.HandleFunc("/retry-status/", retryStatusHandler)
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/ws", websocketHandler)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("smoke upstream listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("smoke upstream ok\n"))
}

func headersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]string{
		"host":              r.Host,
		"x_forwarded_for":   r.Header.Get("X-Forwarded-For"),
		"x_forwarded_host":  r.Header.Get("X-Forwarded-Host"),
		"x_forwarded_proto": r.Header.Get("X-Forwarded-Proto"),
		"x_request_method":  r.Header.Get("X-Request-Method"),
		"x_smoke_request":   r.Header.Get("X-Smoke-Request"),
	})
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(body)
	prefix := body
	if len(prefix) > 256 {
		prefix = prefix[:256]
	}
	writeJSON(w, map[string]any{
		"method":         r.Method,
		"content_length": len(body),
		"sha256":         hex.EncodeToString(sum[:]),
		"prefix":         string(prefix),
	})
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	for i := 1; i <= 5; i++ {
		_, _ = fmt.Fprintf(w, "chunk-%d\n", i)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func slowHeadersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sleep := 3 * time.Second
	if raw := strings.TrimSpace(os.Getenv("SMOKE_SLOW_HEADERS_SLEEP")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			sleep = parsed
		}
	}
	time.Sleep(sleep)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("slow response\n"))
}

func closeEarlyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("close early connection close: %v", err)
		}
	}()
	_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 64\r\n\r\npartial")
	_ = rw.Flush()
}

func retryAssetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	retryAsset.mu.Lock()
	retryAsset.totalAttempts++
	retryAsset.attemptsByPath[r.URL.Path]++
	attempt := retryAsset.attemptsByPath[r.URL.Path]
	retryAsset.mu.Unlock()

	if attempt == 1 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Second):
			http.Error(w, "first retry smoke attempt was not disconnected", http.StatusGatewayTimeout)
			return
		}
	}

	contentType := "application/javascript"
	if strings.HasSuffix(r.URL.Path, ".css") {
		contentType = "text/css"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Smoke-Upstream-Attempt", strconv.Itoa(attempt))
	_, _ = fmt.Fprintf(w, "%s recovered\n", strings.TrimPrefix(r.URL.Path, "/retry-assets/"))
}

func retryAssetStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	retryAsset.mu.Lock()
	attempts := retryAsset.totalAttempts
	retryAsset.mu.Unlock()
	writeJSON(w, map[string]int{"attempts": attempts})
}

func retryStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/retry-status/"), "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	retryCode, err := strconv.Atoi(parts[0])
	if err != nil || retryCode < 400 || retryCode > 599 {
		http.Error(w, "invalid retry status", http.StatusBadRequest)
		return
	}

	retryStatus.mu.Lock()
	retryStatus.attemptsByPath[r.URL.Path]++
	attempt := retryStatus.attemptsByPath[r.URL.Path]
	retryStatus.mu.Unlock()

	w.Header().Set("X-Smoke-Upstream-Attempt", strconv.Itoa(attempt))
	if attempt == 1 {
		http.Error(w, fmt.Sprintf("temporary upstream status %d", retryCode), retryCode)
		return
	}
	if strings.HasSuffix(parts[1], ".css") {
		w.Header().Set("Content-Type", "text/css")
	} else {
		w.Header().Set("Content-Type", "application/javascript")
	}
	_, _ = fmt.Fprintf(w, "%s recovered from %d\n", parts[1], retryCode)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := http.StatusOK
	if raw := strings.TrimSpace(os.Getenv("SMOKE_HEALTH_STATUS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 100 && parsed <= 599 {
			status = parsed
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "health %d\n", status)
}

func websocketHandler(w http.ResponseWriter, r *http.Request) {
	if !headerHasToken(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "websocket key required", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("websocket connection close: %v", err)
		}
	}()

	accept := websocketAccept(key)
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.WriteString("Upgrade: websocket\r\n")
	_, _ = rw.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n")
	_, _ = rw.WriteString("\r\n")
	if err := rw.Flush(); err != nil {
		return
	}

	reader := rw.Reader
	for {
		opcode, payload, err := readWebSocketFrame(reader)
		if err != nil {
			return
		}
		switch opcode {
		case 0x1:
			response := payload
			if string(payload) == "ping" {
				response = []byte("pong")
			}
			if err := writeWebSocketFrame(conn, 0x1, response); err != nil {
				return
			}
		case 0x8:
			_ = writeWebSocketFrame(conn, 0x8, nil)
			return
		case 0x9:
			if err := writeWebSocketFrame(conn, 0xa, payload); err != nil {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func headerHasToken(header http.Header, name string, want string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func readWebSocketFrame(r *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	payloadLen := uint64(header[1] & 0x7f)
	switch payloadLen {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(ext[:])
	}
	if payloadLen > 1<<20 {
		return 0, nil, errors.New("websocket payload too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeWebSocketFrame(conn net.Conn, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(len(payload)))
		header = append(header, ext[:]...)
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}
