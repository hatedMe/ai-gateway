package gateway

import (
	"ai-gateway-go/internal/translate"
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

func (h *handler) conversionError(w http.ResponseWriter, r *http.Request, status int, err error) {
	var conversion *translate.ConversionError
	if !errors.As(err, &conversion) {
		writeError(w, status, errorType(status), err.Error())
		return
	}
	h.logConversionFailure(r, conversion)
	body := errorBody(errorType(status), conversion.Description)
	detail := body["error"].(map[string]any)
	detail["code"], detail["stage"], detail["field"] = conversion.Code, conversion.Stage, conversion.Field
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *handler) logConversionFailure(r *http.Request, err error) {
	setOutcome(r, "conversion_error")
	if r.Context().Err() != nil {
		return
	}
	var conversion *translate.ConversionError
	if !errors.As(err, &conversion) {
		return
	}
	id, _ := r.Context().Value(requestIDKey{}).(string)
	log.Printf("conversion failed request_id=%s upstream=%s upstream_protocol=%s conversion_stage=%s conversion_code=%s", id, h.upstreamName, h.protocol, conversion.Stage, conversion.Code)
}
