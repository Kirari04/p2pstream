package tunnel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

func WriteFrame(w io.Writer, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > MaxControlFrameBytes {
		return fmt.Errorf("control frame exceeds %d bytes", MaxControlFrameBytes)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func ReadFrame(r io.Reader, payload any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > MaxControlFrameBytes {
		return fmt.Errorf("control frame exceeds %d bytes", MaxControlFrameBytes)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, payload)
}

func WriteOpenRequest(w io.Writer, req OpenRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return WriteFrame(w, req)
}

func ReadOpenRequest(r io.Reader) (OpenRequest, error) {
	var req OpenRequest
	if err := ReadFrame(r, &req); err != nil {
		return OpenRequest{}, err
	}
	if err := req.Validate(); err != nil {
		return OpenRequest{}, err
	}
	return req, nil
}

func WriteOpenResponse(w io.Writer, resp OpenResponse) error {
	return WriteFrame(w, resp)
}

func ReadOpenResponse(r io.Reader) (OpenResponse, error) {
	var resp OpenResponse
	if err := ReadFrame(r, &resp); err != nil {
		return OpenResponse{}, err
	}
	return resp, nil
}
