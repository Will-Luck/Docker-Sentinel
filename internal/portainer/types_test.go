package portainer

import "testing"

func TestEndpoint_IsLocalSocket(t *testing.T) {
	tests := []struct {
		name string
		ep   Endpoint
		want bool
	}{
		{
			name: "unix socket URL",
			ep:   Endpoint{URL: "unix:///var/run/docker.sock", Type: EndpointDocker},
			want: true,
		},
		{
			name: "empty URL with Docker type",
			ep:   Endpoint{URL: "", Type: EndpointDocker},
			want: true,
		},
		{
			name: "TCP URL with Docker type",
			ep:   Endpoint{URL: "tcp://192.168.1.100:2375", Type: EndpointDocker},
			want: false,
		},
		{
			name: "agent endpoint",
			ep:   Endpoint{URL: "tcp://192.168.1.100:9001", Type: EndpointAgentDocker},
			want: false,
		},
		{
			name: "empty URL with agent type",
			ep:   Endpoint{URL: "", Type: EndpointAgentDocker},
			want: false,
		},
		{
			name: "kubernetes endpoint",
			ep:   Endpoint{URL: "", Type: EndpointKubernetes},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ep.IsLocalSocket()
			if got != tt.want {
				t.Errorf("IsLocalSocket() = %v, want %v", got, tt.want)
			}
		})
	}
}
