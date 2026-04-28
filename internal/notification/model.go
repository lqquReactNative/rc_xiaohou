package notification

// Request is submitted by business systems to trigger an outbound notification.
type Request struct {
	VendorID string                 `json:"vendor_id"`
	Payload  map[string]interface{} `json:"payload"`
}

// Response is returned to the caller after intake validation succeeds.
type Response struct {
	ID       string `json:"id"`
	VendorID string `json:"vendor_id"`
	Status   string `json:"status"`
}
