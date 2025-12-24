package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMCPCapabilityScanClient_ListCapabilities(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse ManageOrgDataResponse
		serverStatus   int
		wantCount      int
		wantErr        bool
	}{
		{
			name: "successful list with capabilities",
			serverResponse: ManageOrgDataResponse{
				Success: true,
				Data: &struct {
					Result *struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					} `json:"result,omitempty"`
				}{
					Result: &struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					}{
						Success:    true,
						TotalCount: 150,
					},
				},
			},
			serverStatus: http.StatusOK,
			wantCount:    150,
			wantErr:      false,
		},
		{
			name: "successful list with zero capabilities",
			serverResponse: ManageOrgDataResponse{
				Success: true,
				Data: &struct {
					Result *struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					} `json:"result,omitempty"`
				}{
					Result: &struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					}{
						Success:    true,
						TotalCount: 0,
					},
				},
			},
			serverStatus: http.StatusOK,
			wantCount:    0,
			wantErr:      false,
		},
		{
			name: "server error",
			serverResponse: ManageOrgDataResponse{
				Success: false,
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{
					Code:    "500",
					Message: "Internal server error",
				},
			},
			serverStatus: http.StatusOK,
			wantCount:    0,
			wantErr:      true,
		},
		{
			name:           "HTTP error",
			serverResponse: ManageOrgDataResponse{},
			serverStatus:   http.StatusInternalServerError,
			wantCount:      0,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request
				if r.Method != "POST" {
					t.Errorf("Expected POST, got %s", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
				}

				// Parse request body
				var req ManageOrgDataRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}

				// Verify request fields
				if req.DataType != "capabilities" {
					t.Errorf("Expected dataType=capabilities, got %s", req.DataType)
				}
				if req.Operation != "list" {
					t.Errorf("Expected operation=list, got %s", req.Operation)
				}

				w.WriteHeader(tt.serverStatus)
				json.NewEncoder(w).Encode(tt.serverResponse)
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     1,
				InitialBackoff: 10 * time.Millisecond,
			})

			count, err := client.ListCapabilities(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("ListCapabilities() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if count != tt.wantCount {
				t.Errorf("ListCapabilities() count = %v, want %v", count, tt.wantCount)
			}
		})
	}
}

func TestMCPCapabilityScanClient_TriggerFullScan(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse ManageOrgDataResponse
		serverStatus   int
		wantErr        bool
	}{
		{
			name: "successful full scan",
			serverResponse: ManageOrgDataResponse{
				Success: true,
				Data: &struct {
					Result *struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					} `json:"result,omitempty"`
				}{
					Result: &struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					}{
						Success: true,
						Status:  "started",
						Message: "Full capability scan initiated in background",
					},
				},
			},
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
		{
			name: "server returns error",
			serverResponse: ManageOrgDataResponse{
				Success: false,
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{
					Code:    "ERROR",
					Message: "Scan failed",
				},
			},
			serverStatus: http.StatusOK,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ManageOrgDataRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}

				// Verify request fields for full scan
				if req.Operation != "scan" {
					t.Errorf("Expected operation=scan, got %s", req.Operation)
				}
				if req.Mode != "full" {
					t.Errorf("Expected mode=full, got %s", req.Mode)
				}

				w.WriteHeader(tt.serverStatus)
				json.NewEncoder(w).Encode(tt.serverResponse)
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     1,
				InitialBackoff: 10 * time.Millisecond,
			})

			err := client.TriggerFullScan(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("TriggerFullScan() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMCPCapabilityScanClient_TriggerScan(t *testing.T) {
	tests := []struct {
		name           string
		resourceList   string
		serverResponse ManageOrgDataResponse
		serverStatus   int
		wantErr        bool
	}{
		{
			name:         "successful targeted scan",
			resourceList: "RDSInstance.database.aws.crossplane.io",
			serverResponse: ManageOrgDataResponse{
				Success: true,
				Data: &struct {
					Result *struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					} `json:"result,omitempty"`
				}{
					Result: &struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					}{
						Success: true,
						Status:  "started",
						Message: "Scan initiated for 1 resources",
					},
				},
			},
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
		{
			name:         "multiple resources",
			resourceList: "RDSInstance.database.aws.crossplane.io,Bucket.s3.aws.crossplane.io",
			serverResponse: ManageOrgDataResponse{
				Success: true,
			},
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ManageOrgDataRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}

				// Verify request fields for targeted scan
				if req.Operation != "scan" {
					t.Errorf("Expected operation=scan, got %s", req.Operation)
				}
				if req.ResourceList != tt.resourceList {
					t.Errorf("Expected resourceList=%s, got %s", tt.resourceList, req.ResourceList)
				}
				if req.Mode != "" {
					t.Errorf("Expected empty mode for targeted scan, got %s", req.Mode)
				}

				w.WriteHeader(tt.serverStatus)
				json.NewEncoder(w).Encode(tt.serverResponse)
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     1,
				InitialBackoff: 10 * time.Millisecond,
			})

			err := client.TriggerScan(context.Background(), tt.resourceList)

			if (err != nil) != tt.wantErr {
				t.Errorf("TriggerScan() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMCPCapabilityScanClient_DeleteCapability(t *testing.T) {
	tests := []struct {
		name           string
		capabilityID   string
		serverResponse ManageOrgDataResponse
		serverStatus   int
		wantErr        bool
	}{
		{
			name:         "successful delete",
			capabilityID: "RDSInstance.database.aws.crossplane.io",
			serverResponse: ManageOrgDataResponse{
				Success: true,
				Data: &struct {
					Result *struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					} `json:"result,omitempty"`
				}{
					Result: &struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					}{
						Success:   true,
						Operation: "delete",
						Message:   "Capability deleted successfully",
					},
				},
			},
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
		{
			name:         "delete not found",
			capabilityID: "NonExistent.example.com",
			serverResponse: ManageOrgDataResponse{
				Success: false,
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{
					Code:    "NOT_FOUND",
					Message: "Capability not found",
				},
			},
			serverStatus: http.StatusOK,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ManageOrgDataRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}

				// Verify request fields for delete
				if req.Operation != "delete" {
					t.Errorf("Expected operation=delete, got %s", req.Operation)
				}
				if req.ID != tt.capabilityID {
					t.Errorf("Expected id=%s, got %s", tt.capabilityID, req.ID)
				}

				w.WriteHeader(tt.serverStatus)
				json.NewEncoder(w).Encode(tt.serverResponse)
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     1,
				InitialBackoff: 10 * time.Millisecond,
			})

			err := client.DeleteCapability(context.Background(), tt.capabilityID)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteCapability() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMCPCapabilityScanClient_RetryBehavior(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Fail first 2 attempts
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ManageOrgDataResponse{
				Success: false,
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{
					Code:    "500",
					Message: "Temporary error",
				},
			})
			return
		}
		// Succeed on 3rd attempt
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ManageOrgDataResponse{
			Success: true,
			Data: &struct {
				Result *struct {
					Success      bool          `json:"success"`
					Status       string        `json:"status,omitempty"`
					Message      string        `json:"message,omitempty"`
					Capabilities []interface{} `json:"capabilities,omitempty"`
					TotalCount   int           `json:"totalCount,omitempty"`
					Operation    string        `json:"operation,omitempty"`
				} `json:"result,omitempty"`
			}{
				Result: &struct {
					Success      bool          `json:"success"`
					Status       string        `json:"status,omitempty"`
					Message      string        `json:"message,omitempty"`
					Capabilities []interface{} `json:"capabilities,omitempty"`
					TotalCount   int           `json:"totalCount,omitempty"`
					Operation    string        `json:"operation,omitempty"`
				}{
					Success:    true,
					TotalCount: 10,
				},
			},
		})
	}))
	defer server.Close()

	client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
		Endpoint:       server.URL,
		Collection:     "test-capabilities",
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	count, err := client.ListCapabilities(context.Background())

	if err != nil {
		t.Errorf("Expected success after retries, got error: %v", err)
	}
	if count != 10 {
		t.Errorf("Expected count=10, got %d", count)
	}
	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestMCPCapabilityScanClient_EndpointConstruction(t *testing.T) {
	tests := []struct {
		name             string
		inputEndpoint    string
		expectedEndpoint string
	}{
		{
			name:             "base URL",
			inputEndpoint:    "http://mcp:8080",
			expectedEndpoint: "http://mcp:8080/api/v1/tools/manageOrgData",
		},
		{
			name:             "base URL with trailing slash",
			inputEndpoint:    "http://mcp:8080/",
			expectedEndpoint: "http://mcp:8080/api/v1/tools/manageOrgData",
		},
		{
			name:             "full endpoint already specified",
			inputEndpoint:    "http://mcp:8080/api/v1/tools/manageOrgData",
			expectedEndpoint: "http://mcp:8080/api/v1/tools/manageOrgData",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint: tt.inputEndpoint,
			})

			if client.endpoint != tt.expectedEndpoint {
				t.Errorf("endpoint = %s, want %s", client.endpoint, tt.expectedEndpoint)
			}
		})
	}
}

func TestManageOrgDataResponse_GetErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		response ManageOrgDataResponse
		want     string
	}{
		{
			name: "error with message",
			response: ManageOrgDataResponse{
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{
					Code:    "ERROR",
					Message: "Something went wrong",
				},
			},
			want: "Something went wrong",
		},
		{
			name: "error with code only",
			response: ManageOrgDataResponse{
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{
					Code: "ERR_500",
				},
			},
			want: "error code: ERR_500",
		},
		{
			name: "message in result",
			response: ManageOrgDataResponse{
				Data: &struct {
					Result *struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					} `json:"result,omitempty"`
				}{
					Result: &struct {
						Success      bool          `json:"success"`
						Status       string        `json:"status,omitempty"`
						Message      string        `json:"message,omitempty"`
						Capabilities []interface{} `json:"capabilities,omitempty"`
						TotalCount   int           `json:"totalCount,omitempty"`
						Operation    string        `json:"operation,omitempty"`
					}{
						Message: "Result message",
					},
				},
			},
			want: "Result message",
		},
		{
			name:     "no error info",
			response: ManageOrgDataResponse{},
			want:     "unknown error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.response.GetErrorMessage()
			if got != tt.want {
				t.Errorf("GetErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}
