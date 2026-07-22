package server

import (
	"net/http"

	"github.com/aisphereio/kernel/encodingx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"google.golang.org/protobuf/proto"
)

// protoJSONResponseEncoder is the Hub's custom response encoder. When the
// response value is a proto.Message (i.e. every generated proto HTTP handler
// that calls ctx.Result), it is always encoded with the protojson codec
// regardless of the client's Accept header.
//
// This fixes two problems caused by the kernel's DefaultResponseEncoder
// falling back to the plain "json" codec (encoding/json) when Accept is absent:
//
//  1. Empty repeated fields are dropped by protobuf's `omitempty` struct tag,
//     so ListClusters returns {} instead of {"clusters":[],"nextPageToken":""}.
//  2. Field names use the snake_case struct tags (display_name, next_page_token)
//     instead of the camelCase json_name the OpenAPI spec and generated TS
//     types expect (displayName, nextPageToken).
//
// The protojson codec (encodingx/protojson) uses EmitUnpopulated: true and
// proto JSON field names (camelCase), matching the contract the generated Go
// HTTP client already relies on (it sets Accept: application/protojson).
//
// Non-proto values fall through to DefaultResponseEncoder, preserving the
// existing behaviour for any future httpBody / redirect responses.
func protoJSONResponseEncoder(w http.ResponseWriter, r *http.Request, v any) error {
	if v == nil {
		return nil
	}
	if msg, ok := v.(proto.Message); ok {
		codec := encodingx.GetCodec("protojson")
		data, err := codec.Marshal(msg)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/protojson")
		_, err = w.Write(data)
		return err
	}
	// Non-proto values (httpBody, Redirector, or raw types) use the default
	// encoder, which negotiates via Accept and falls back to plain json.
	return khttp.DefaultResponseEncoder(w, r, v)
}
