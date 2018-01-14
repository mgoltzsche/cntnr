package model

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
)

type PathResolver interface {
	ResolveFile(string) string
}

type pathResolver string

func NewPathResolver(baseDir string) PathResolver {
	return pathResolver(baseDir)
}

func (self pathResolver) ResolveFile(file string) string {
	baseDir := string(self)
	file = filepath.Clean(file)
	if !filepath.IsAbs(file) && !(file == "~" || len(file) > 1 && file[0:2] == "~/") {
		file = filepath.Join(baseDir, file)
	}
	return file
}

type ResourceResolver interface {
	PathResolver
	ResolveMountSource(VolumeMount) (string, error)
}

type resourceResolver struct {
	PathResolver
	volumes map[string]Volume
}

func NewResourceResolver(paths PathResolver, vols map[string]Volume) ResourceResolver {
	return &resourceResolver{paths, vols}
}

func (self *resourceResolver) ResolveMountSource(m VolumeMount) (src string, err error) {
	if m.IsNamedVolume() {
		src, err = self.named(m.Source)
	} else if m.Source == "" {
		src = self.anonymous(m.Target)
	} else {
		src = self.path(m.Source)
	}
	return
}

func (self *resourceResolver) named(src string) (string, error) {
	r, ok := self.volumes[src]
	if !ok {
		return "", fmt.Errorf("volume %q not found", src)
	}
	if r.Source == "" {
		return self.anonymous("!" + src), nil
	}
	return r.Source, nil
}

func (self *resourceResolver) anonymous(id string) string {
	id = filepath.Clean(id)
	return filepath.Join("volumes", base64.RawStdEncoding.EncodeToString([]byte(id)))
}

func (self *resourceResolver) path(file string) string {
	return self.ResolveFile(file)
}
