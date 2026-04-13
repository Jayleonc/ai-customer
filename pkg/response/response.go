// Package response provides standard API response structures
package response

// JSONResponse defines the standard structure for API responses
// code: 0 success, other values indicate error
// msg: message string
// data: payload

type JSONResponse struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

// Success creates a success response with data
func Success(data interface{}) JSONResponse {
	return JSONResponse{Code: 0, Msg: "ok", Data: data}
}

// Error creates an error response with message and optional code
func Error(code int, msg string) JSONResponse {
	if code == 0 {
		code = 1
	}
	return JSONResponse{Code: code, Msg: msg}
}
