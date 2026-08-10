package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/utils/ptr"
)

func TestMCPCapabilityScanClient_ListCapabilities(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse string // JSON response
		serverStatus   int
		wantCount      int
		wantErr        bool
	}{
		{
			name: "successful list with capabilities",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"data": {
							"totalCount": 150,
							"returnedCount": 150
						}
					}
				}
			}`,
			serverStatus: http.StatusOK,
			wantCount:    150,
			wantErr:      false,
		},
		{
			name: "successful list with zero capabilities",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"data": {
							"totalCount": 0,
							"returnedCount": 0
						}
					}
				}
			}`,
			serverStatus: http.StatusOK,
			wantCount:    0,
			wantErr:      false,
		},
		{
			name: "server error",
			serverResponse: `{
				"success": false,
				"error": {
					"code": "500",
					"message": "Internal server error"
				}
			}`,
			serverStatus: http.StatusOK,
			wantCount:    0,
			wantErr:      true,
		},
		{
			name:           "HTTP error",
			serverResponse: `{}`,
			serverStatus:   http.StatusInternalServerError,
			wantCount:      0,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("Expected POST, got %s", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
				}

				var req ManageOrgDataRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}
				if req.DataType != "capabilities" {
					t.Errorf("Expected dataType=capabilities, got %s", req.DataType)
				}
				if req.Operation != "list" {
					t.Errorf("Expected operation=list, got %s", req.Operation)
				}

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     ptr.To(0),
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

func TestMCPCapabilityScanClient_ListCapabilityInfos(t *testing.T) {
	tests := []struct {
		name              string
		serverResponse    string // JSON response
		serverStatus      int
		wantResourceNames []string
		wantIDs           []string
		wantComplete      bool
		wantErr           bool
	}{
		{
			name: "successful complete list",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"data": {
							"capabilities": [
								{"id": "1111", "resourceName": "RDSInstance.database.aws.crossplane.io"},
								{"id": "2222", "resourceName": "Bucket.s3.aws.crossplane.io"},
								{"id": "3333", "resourceName": "Deployment.apps"}
							],
							"totalCount": 3,
							"returnedCount": 3
						}
					}
				}
			}`,
			serverStatus:      http.StatusOK,
			wantResourceNames: []string{"RDSInstance.database.aws.crossplane.io", "Bucket.s3.aws.crossplane.io", "Deployment.apps"},
			wantIDs:           []string{"1111", "2222", "3333"},
			wantComplete:      true,
			wantErr:           false,
		},
		{
			name: "truncated list is incomplete",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"data": {
							"capabilities": [
								{"id": "1111", "resourceName": "RDSInstance.database.aws.crossplane.io"}
							],
							"totalCount": 250,
							"returnedCount": 100
						}
					}
				}
			}`,
			serverStatus:      http.StatusOK,
			wantResourceNames: []string{"RDSInstance.database.aws.crossplane.io"},
			wantIDs:           []string{"1111"},
			wantComplete:      false,
			wantErr:           false,
		},
		{
			name: "empty collection is complete",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"data": {
							"capabilities": [],
							"totalCount": 0,
							"returnedCount": 0
						}
					}
				}
			}`,
			serverStatus:      http.StatusOK,
			wantResourceNames: []string{},
			wantComplete:      true,
			wantErr:           false,
		},
		{
			name: "inner success false surfaces as error",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": false,
						"message": "Vector DB (Qdrant) connection required"
					}
				}
			}`,
			serverStatus:      http.StatusOK,
			wantResourceNames: nil,
			wantComplete:      false,
			wantErr:           true,
		},
		{
			name: "envelope error",
			serverResponse: `{
				"success": false,
				"error": {
					"code": "500",
					"message": "Internal server error"
				}
			}`,
			serverStatus:      http.StatusOK,
			wantResourceNames: nil,
			wantComplete:      false,
			wantErr:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ManageOrgDataRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}
				if req.DataType != "capabilities" {
					t.Errorf("Expected dataType=capabilities, got %s", req.DataType)
				}
				if req.Operation != "list" {
					t.Errorf("Expected operation=list, got %s", req.Operation)
				}
				// ListCapabilityInfos should use a large limit
				if req.Limit < 1000 {
					t.Errorf("Expected large limit for ListCapabilityInfos, got %d", req.Limit)
				}

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     ptr.To(0),
				InitialBackoff: 10 * time.Millisecond,
			})

			caps, complete, err := client.ListCapabilityInfos(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("ListCapabilityInfos() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if complete != tt.wantComplete {
				t.Errorf("ListCapabilityInfos() complete = %v, want %v", complete, tt.wantComplete)
			}
			if len(caps) != len(tt.wantResourceNames) {
				t.Errorf("ListCapabilityInfos() returned %d capabilities, want %d", len(caps), len(tt.wantResourceNames))
			}
			for i, c := range caps {
				if i < len(tt.wantResourceNames) && c.ResourceName != tt.wantResourceNames[i] {
					t.Errorf("ListCapabilityInfos()[%d].ResourceName = %s, want %s", i, c.ResourceName, tt.wantResourceNames[i])
				}
				if i < len(tt.wantIDs) && c.ID != tt.wantIDs[i] {
					t.Errorf("ListCapabilityInfos()[%d].ID = %s, want %s", i, c.ID, tt.wantIDs[i])
				}
			}
		})
	}
}

func TestMCPCapabilityScanClient_TriggerFullScan(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse string
		serverStatus   int
		wantErr        bool
	}{
		{
			name: "successful full scan",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"status": "started",
						"message": "Full capability scan initiated in background"
					}
				}
			}`,
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
		{
			name: "envelope error",
			serverResponse: `{
				"success": false,
				"error": {
					"code": "ERROR",
					"message": "Scan failed"
				}
			}`,
			serverStatus: http.StatusOK,
			wantErr:      true,
		},
		{
			name: "inner result failure",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": false,
						"message": "Vector DB (Qdrant) connection required"
					}
				}
			}`,
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
				if req.Operation != "scan" {
					t.Errorf("Expected operation=scan, got %s", req.Operation)
				}
				if req.Mode != "full" {
					t.Errorf("Expected mode=full, got %s", req.Mode)
				}

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     ptr.To(0),
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
		serverResponse string
		serverStatus   int
		wantErr        bool
	}{
		{
			name:         "successful targeted scan",
			resourceList: "RDSInstance.database.aws.crossplane.io",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"status": "started",
						"message": "Scan initiated for 1 resources"
					}
				}
			}`,
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
		{
			name:         "multiple resources",
			resourceList: "RDSInstance.database.aws.crossplane.io,Bucket.s3.aws.crossplane.io",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"status": "started",
						"message": "Scan initiated for 2 resources"
					}
				}
			}`,
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
		{
			name:           "missing nested result is a failure",
			resourceList:   "RDSInstance.database.aws.crossplane.io",
			serverResponse: `{"success": true}`,
			serverStatus:   http.StatusOK,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ManageOrgDataRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}
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
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     ptr.To(0),
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
		serverResponse string
		serverStatus   int
		wantErr        bool
	}{
		{
			name:         "successful delete",
			capabilityID: "1111-2222-3333",
			serverResponse: `{
				"success": true,
				"data": {
					"result": {
						"success": true,
						"operation": "delete",
						"message": "Capability deleted successfully"
					}
				}
			}`,
			serverStatus: http.StatusOK,
			wantErr:      false,
		},
		{
			name:         "delete not found",
			capabilityID: "does-not-exist",
			serverResponse: `{
				"success": false,
				"error": {
					"code": "NOT_FOUND",
					"message": "Capability not found"
				}
			}`,
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
				if req.Operation != "delete" {
					t.Errorf("Expected operation=delete, got %s", req.Operation)
				}
				if req.ID != tt.capabilityID {
					t.Errorf("Expected id=%s, got %s", tt.capabilityID, req.ID)
				}

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
				Endpoint:       server.URL,
				Collection:     "test-capabilities",
				MaxRetries:     ptr.To(0),
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
			_, _ = w.Write([]byte(`{
				"success": false,
				"error": {"code": "500", "message": "Temporary error"}
			}`))
			return
		}
		// Succeed on 3rd attempt
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": {
				"result": {
					"success": true,
					"data": {"totalCount": 10, "returnedCount": 10}
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
		Endpoint:       server.URL,
		Collection:     "test-capabilities",
		MaxRetries:     ptr.To(3),
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

func TestMCPCapabilityScanClient_RetriesOnInnerFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		// Envelope always succeeds, but the operation result reports a failure.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": {
				"result": {
					"success": false,
					"message": "Vector DB (Qdrant) connection required"
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
		Endpoint:       server.URL,
		Collection:     "test-capabilities",
		MaxRetries:     ptr.To(2),
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	_, _, err := client.ListCapabilityInfos(context.Background())

	if err == nil {
		t.Errorf("Expected error when inner result reports failure, got nil")
	}
	// initial attempt + 2 retries
	if attempts != 3 {
		t.Errorf("Expected 3 attempts on inner failure, got %d", attempts)
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
				Error: &ManageOrgDataError{
					Code:    "ERROR",
					Message: "Something went wrong",
				},
			},
			want: "Something went wrong",
		},
		{
			name: "error with code only",
			response: ManageOrgDataResponse{
				Error: &ManageOrgDataError{
					Code: "ERR_500",
				},
			},
			want: "error code: ERR_500",
		},
		{
			name: "message in result",
			response: ManageOrgDataResponse{
				Data: &ManageOrgDataEnvelope{
					Result: &ManageOrgDataResult{
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

func TestManageOrgDataResponse_IsListComplete(t *testing.T) {
	tests := []struct {
		name     string
		response ManageOrgDataResponse
		want     bool
	}{
		{
			name: "returned equals total",
			response: ManageOrgDataResponse{
				Data: &ManageOrgDataEnvelope{Result: &ManageOrgDataResult{
					Data: &ManageOrgDataListData{TotalCount: ptr.To(5), ReturnedCount: ptr.To(5)},
				}},
			},
			want: true,
		},
		{
			name: "returned less than total",
			response: ManageOrgDataResponse{
				Data: &ManageOrgDataEnvelope{Result: &ManageOrgDataResult{
					Data: &ManageOrgDataListData{TotalCount: ptr.To(250), ReturnedCount: ptr.To(100)},
				}},
			},
			want: false,
		},
		{
			name: "empty collection",
			response: ManageOrgDataResponse{
				Data: &ManageOrgDataEnvelope{Result: &ManageOrgDataResult{
					Data: &ManageOrgDataListData{TotalCount: ptr.To(0), ReturnedCount: ptr.To(0)},
				}},
			},
			want: true,
		},
		{
			name: "omitted counts is not complete",
			response: ManageOrgDataResponse{
				Data: &ManageOrgDataEnvelope{Result: &ManageOrgDataResult{
					Data: &ManageOrgDataListData{},
				}},
			},
			want: false,
		},
		{
			name:     "no data is not complete",
			response: ManageOrgDataResponse{},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.response.IsListComplete(); got != tt.want {
				t.Errorf("IsListComplete() = %v, want %v", got, tt.want)
			}
		})
	}
}
