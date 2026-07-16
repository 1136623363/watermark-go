package parser

import "strings"

type PlatformKey string

type Capability uint32

const (
	CapabilityVideo Capability = 1 << iota
	CapabilityGallery
	CapabilityAudio
	CapabilityLivePhoto
	CapabilityM3U8
)

type HostRule struct {
	Host              string `json:"host"`
	IncludeSubdomains bool   `json:"includeSubdomains"`
}

type Descriptor struct {
	Key          PlatformKey
	DisplayName  string
	Aliases      []PlatformKey
	HostRules    []HostRule
	Capabilities Capability
	Priority     int
	QueryKeys    []string
	SupportsID   bool
	MaxRequests  int
	MaxRedirects int
	// SessionHost is the explicit exact upstream authority to which short-lived
	// material is sent. It is independent of the user's share URL host. Empty
	// means that the descriptor does not consume short-lived session material.
	SessionHost string
	New         func(Dependencies) (Parser, error)
}

func (descriptor Descriptor) hasCapability(capability Capability) bool {
	return descriptor.Capabilities&capability != 0
}

func validPlatformKey(key PlatformKey) bool {
	value := string(key)
	if value == "" {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || (index > 0 && character == '-') {
			continue
		}
		return false
	}
	return strings.TrimSpace(value) == value
}
