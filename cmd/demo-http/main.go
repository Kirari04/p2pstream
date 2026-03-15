package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/http/httputil"

	"github.com/google/uuid"
	"p2pstream/httpmsg"
	"p2pstream/msg"
)

type InterceptedChunk struct {
	ID          string
	TypeString  string
	ChunkNr     uint32
	BodyLen     uint32
	Headers     map[string]string
	BodyPreview string
}

type TemplateData struct {
	Method      string
	Chunks      []InterceptedChunk
	DecodedDump string
}

var htmlTmpl = `
<!DOCTYPE html>
<html>
<head>
    <title>httpmsg Protocol Demo</title>
    <style>
        body { font-family: sans-serif; margin: 2rem; background: #f4f4f9; }
        .container { max-width: 900px; margin: auto; background: white; padding: 2rem; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1, h2, h3 { color: #333; }
        form { margin-bottom: 2rem; padding: 1.5rem; border: 1px solid #ddd; border-radius: 4px; background: #fafafa; }
        .chunk { margin-bottom: 1rem; border: 1px solid #007bff; border-radius: 4px; overflow: hidden; }
        .chunk-header { background: #007bff; color: white; padding: 0.5rem 1rem; display: flex; justify-content: space-between; }
        .chunk-body { padding: 1rem; background: #f8f9fa; font-family: monospace; white-space: pre-wrap; word-break: break-all; }
        .meta { font-size: 0.9em; color: #555; line-height: 1.6; }
        .meta strong { color: #222; }
        pre { background: #2b2b2b; color: #a9b7c6; padding: 1rem; border-radius: 4px; overflow-x: auto; }
        input[type=text], input[type=file] { padding: 0.5rem; border: 1px solid #ccc; border-radius: 3px; }
        button { padding: 0.6rem 2rem; background: #28a745; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 1rem; }
        button:hover { background: #218838; }
    </style>
</head>
<body>
    <div class="container">
        <h1>httpmsg Protocol Demo</h1>
        
        <h2>1. Send a Request</h2>
        <form action="/upload" method="POST" enctype="multipart/form-data">
            <div style="margin-bottom: 1rem;">
                <label style="display:block; font-weight:bold; margin-bottom: 0.5rem;">Text Field:</label>
                <input type="text" name="text_field" value="Hello P2PStream Protocol!" style="width: 100%; box-sizing: border-box;">
            </div>
            <div style="margin-bottom: 1.5rem;">
                <label style="display:block; font-weight:bold; margin-bottom: 0.5rem;">File Upload (Try a large file > 60KB to see chunking):</label>
                <input type="file" name="file_field">
            </div>
            <button type="submit">Submit Request</button>
        </form>

        {{if .Method}}
        <h2>2. Encoder: Chunking Process</h2>
        <p>The original HTTP <strong>{{.Method}}</strong> request was broken down into <strong>{{len .Chunks}}</strong> binary message(s) for the <code>p2pstream</code> protocol.</p>
        
        {{range $index, $chunk := .Chunks}}
        <div class="chunk">
            <div class="chunk-header">
                <span>Message #{{$index}}</span>
                <span>Type: <strong>{{$chunk.TypeString}}</strong></span>
            </div>
            <div class="chunk-body">
                <div class="meta">
                    <strong>ID:</strong> {{$chunk.ID}}<br>
                    {{if eq $chunk.TypeString "RequestTypeBody" "RequestTypeHeaderAndBody"}}
                        {{if eq $chunk.TypeString "RequestTypeBody"}}
                            <strong>Chunk Nr:</strong> {{$chunk.ChunkNr}}<br>
                        {{end}}
                        <strong>Body Length:</strong> {{$chunk.BodyLen}} bytes<br>
                    {{end}}
                    {{if gt (len $chunk.Headers) 0}}
                        <strong>Headers:</strong> {{len $chunk.Headers}} total
                    {{end}}
                </div>
                {{if $chunk.BodyPreview}}
                <hr style="border: 0; border-top: 1px solid #ddd; margin: 1rem 0;">
                <div style="color: #666; margin-bottom: 0.5rem;"><strong>Body Preview (truncated for UI):</strong></div>
                <div style="color: #d63384;">{{$chunk.BodyPreview}}</div>
                {{end}}
            </div>
        </div>
        {{end}}

        <h2>3. Decoder: Reconstructed HTTP Request</h2>
        <p>This is what the decoder reconstructed from the chunks (dumped via <code>httputil.DumpRequest</code>). It is perfectly intact:</p>
        <pre>{{.DecodedDump}}</pre>
        {{end}}
    </div>
</body>
</html>
`

func typeToString(t msg.RequestType) string {
	switch t {
	case msg.RequestTypeHeader:
		return "RequestTypeHeader"
	case msg.RequestTypeBody:
		return "RequestTypeBody"
	case msg.RequestTypeHeaderAndBody:
		return "RequestTypeHeaderAndBody"
	}
	return "Unknown"
}

// ArrayStream implements httpmsg.MessageStream over an array of pre-collected chunks
type ArrayStream struct {
	messages []*msg.Request
	pos      int
}

func (s *ArrayStream) Next() (*msg.Request, error) {
	if s.pos >= len(s.messages) {
		return nil, io.EOF
	}
	m := s.messages[s.pos]
	s.pos++
	return m, nil
}

func main() {
	tmpl := template.Must(template.New("index").Parse(htmlTmpl))

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			http.NotFound(w, req)
			return
		}
		// Render empty form
		tmpl.Execute(w, TemplateData{})
	})

	http.HandleFunc("/upload", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Protect against OOM from malicious large uploads in this demo (Max 5MB)
		req.Body = http.MaxBytesReader(w, req.Body, 5<<20)

		id, err := uuid.NewV7()
		if err != nil {
			http.Error(w, "Failed to generate UUID", http.StatusInternalServerError)
			return
		}

		// 1. Encode the incoming HTTP request
		enc := httpmsg.NewRequestEncoder(id, req)

		var msgs []*msg.Request
		var intercepted []InterceptedChunk

		// Intercept the stream of chunks
		for {
			m, err := enc.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				// If the user uploads more than 5MB, http.MaxBytesReader will return an error here
				http.Error(w, fmt.Sprintf("Encoder error: %v", err), http.StatusInternalServerError)
				return
			}

			// Read body to memory so we can show a preview, but re-attach it for the Decoder
			var bodyData []byte
			var preview string
			if m.Body != nil {
				bodyData, _ = io.ReadAll(m.Body)
				
				// Truncate for preview
				previewLen := len(bodyData)
				if previewLen > 200 {
					previewLen = 200
					preview = string(bodyData[:previewLen]) + " ... (truncated)"
				} else {
					preview = string(bodyData)
				}
				
				// Re-attach body for decoder
				m.Body = bytes.NewReader(bodyData)
			}

			intercepted = append(intercepted, InterceptedChunk{
				ID:          m.ID.String(),
				TypeString:  typeToString(m.Type),
				ChunkNr:     m.ChunkNr,
				BodyLen:     m.BodyLen,
				Headers:     m.Headers,
				BodyPreview: preview,
			})
			msgs = append(msgs, m)
		}

		// 2. Decode the intercepted stream back into an HTTP request
		if len(msgs) == 0 {
			http.Error(w, "No messages generated by encoder", http.StatusInternalServerError)
			return
		}

		stream := &ArrayStream{messages: msgs[1:]} // remaining messages
		decodedReq, err := httpmsg.DecodeRequest(msgs[0], stream)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to decode request: %v", err), http.StatusInternalServerError)
			return
		}

		// Read the full body of the decoded request to ensure stream completion
		// In a real proxy, you'd stream this out directly, but we want to dump it.
		decodedBodyBytes, err := io.ReadAll(decodedReq.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read decoded body: %v", err), http.StatusInternalServerError)
			return
		}
		decodedReq.Body = io.NopCloser(bytes.NewReader(decodedBodyBytes))

		// Dump for verification
		dump, err := httputil.DumpRequest(decodedReq, true)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to dump decoded request: %v", err), http.StatusInternalServerError)
			return
		}

		// 3. Render the UI
		data := TemplateData{
			Method:      req.Method,
			Chunks:      intercepted,
			DecodedDump: string(dump),
		}

		w.Header().Set("Content-Type", "text/html")
		tmpl.Execute(w, data)
	})

	port := ":8080"
	log.Printf("Demo HTTP server running on http://localhost%s", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
