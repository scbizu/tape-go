package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type ScriptArtifact struct {
	Path    string
	Hash    string
	Content string
}

type ScriptBuilder struct {
	baseDir string
}

func NewScriptBuilder(baseDir string) *ScriptBuilder {
	return &ScriptBuilder{baseDir: baseDir}
}

func (b *ScriptBuilder) Build(plan ExecutionPlan) (*ScriptArtifact, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}

	var body strings.Builder
	body.WriteString("#!/usr/bin/env bash\n")
	body.WriteString("set -euo pipefail\n")
	for _, step := range plan.Steps {
		body.WriteString(step.Script)
		body.WriteString("\n")
	}
	content := body.String()
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	return &ScriptArtifact{
		Path:    filepath.Join(b.baseDir, fmt.Sprintf("executor-%s-%d.sh", hash[:12], time.Now().UnixNano())),
		Hash:    hash,
		Content: content,
	}, nil
}
