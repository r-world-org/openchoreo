// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

const writeRevisionPrefix = "rb-sha256:"

type semanticWriteRevisionInput struct {
	UID         string                                `json:"uid"`
	Labels      map[string]string                     `json:"labels,omitempty"`
	Annotations map[string]string                     `json:"annotations,omitempty"`
	Spec        openchoreov1alpha1.ReleaseBindingSpec `json:"spec"`
}

// SemanticWriteRevision returns the revision of the client-writable ReleaseBinding state.
func SemanticWriteRevision(rb *openchoreov1alpha1.ReleaseBinding) (string, error) {
	if rb == nil {
		return "", fmt.Errorf("release binding cannot be nil")
	}

	input := semanticWriteRevisionInput{
		UID:         string(rb.UID),
		Labels:      rb.Labels,
		Annotations: rb.Annotations,
		Spec:        rb.Spec,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal release binding write revision input: %w", err)
	}

	// Decode and re-encode once so embedded RawExtension JSON is canonicalized too.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var canonicalInput any
	if err := decoder.Decode(&canonicalInput); err != nil {
		return "", fmt.Errorf("canonicalize release binding write revision input: %w", err)
	}
	canonical, err := json.Marshal(canonicalInput)
	if err != nil {
		return "", fmt.Errorf("marshal canonical release binding write revision input: %w", err)
	}

	digest := sha256.Sum256(canonical)
	return writeRevisionPrefix + hex.EncodeToString(digest[:]), nil
}

// ParseWriteRevision accepts exactly one unquoted ReleaseBinding write revision.
func ParseWriteRevision(value string) (string, error) {
	const digestLength = sha256.Size * 2
	if len(value) != len(writeRevisionPrefix)+digestLength {
		return "", ErrInvalidReleaseBindingWriteRevision
	}
	digest := value[len(writeRevisionPrefix):]
	if value[:len(writeRevisionPrefix)] != writeRevisionPrefix || !isLowerHex(digest) {
		return "", ErrInvalidReleaseBindingWriteRevision
	}
	return value, nil
}

func isLowerHex(value string) bool {
	for i := range len(value) {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}
