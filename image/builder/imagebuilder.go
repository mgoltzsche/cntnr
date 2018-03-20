package builder

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/mgoltzsche/cntnr/bundle"
	"github.com/mgoltzsche/cntnr/image"
	"github.com/mgoltzsche/cntnr/pkg/log"
	"github.com/mgoltzsche/cntnr/run"
	"github.com/mgoltzsche/cntnr/run/factory"
	"github.com/opencontainers/go-digest"
	ispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-tools/generate"
	"github.com/pkg/errors"
)

type ImageBuilder struct {
	steps []func(*BuildState) error
}

func NewImageBuilder() *ImageBuilder {
	return &ImageBuilder{}
}

func (b *ImageBuilder) FromImage(image string) {
	b.addBuildStep(func(builder *BuildState) error {
		return builder.FromImage(image)
	})
}

func (b *ImageBuilder) SetAuthor(image string) {
	b.addBuildStep(func(builder *BuildState) error {
		builder.SetAuthor(image)
		return nil
	})
}

func (b *ImageBuilder) SetWorkingDir(dir string) {
	b.addBuildStep(func(builder *BuildState) error {
		return builder.SetWorkingDir(dir)
	})
}

func (b *ImageBuilder) SetEntrypoint(entrypoint []string) {
	b.addBuildStep(func(builder *BuildState) error {
		return builder.SetEntrypoint(entrypoint)
	})
}

func (b *ImageBuilder) SetCmd(cmd []string) {
	b.addBuildStep(func(builder *BuildState) error {
		return builder.SetCmd(cmd)
	})
}

func (b *ImageBuilder) Run(cmd string) {
	b.addBuildStep(func(builder *BuildState) error {
		return builder.Run(cmd)
	})
}

func (b *ImageBuilder) Tag(tag string) {
	b.addBuildStep(func(builder *BuildState) error {
		return builder.Tag(tag)
	})
}

func (b *ImageBuilder) addBuildStep(step func(*BuildState) error) {
	b.steps = append(b.steps, step)
}

func (b *ImageBuilder) Build(images image.ImageStoreRW, bundles bundle.BundleStore, cache ImageBuildCache, rootless bool, proot string, loggers log.Loggers) (img image.Image, err error) {
	defer func() {
		if err != nil {
			err = errors.Wrap(err, "build image")
		}
	}()
	state := NewBuildState(images, bundles, cache, rootless, proot, loggers)
	defer func() {
		if e := state.Close(); e != nil {
			err = multierror.Append(err, e)
		}
	}()

	now := time.Now()
	state.config.Created = &now
	state.config.Architecture = runtime.GOARCH
	state.config.OS = runtime.GOOS

	if len(b.steps) == 0 {
		return img, errors.New("no build steps defined")
	}

	for _, step := range b.steps {
		if err = step(&state); err != nil {
			return
		}
	}

	return *state.image, nil
}

type BuildState struct {
	images   image.ImageStoreRW
	bundles  bundle.BundleStore
	config   ispecs.Image
	image    *image.Image
	cache    ImageBuildCache
	bundle   *bundle.LockedBundle
	rootless bool
	proot    string
	loggers  log.Loggers
}

func NewBuildState(images image.ImageStoreRW, bundles bundle.BundleStore, cache ImageBuildCache, rootless bool, proot string, loggers log.Loggers) (r BuildState) {
	r.images = images
	r.bundles = bundles
	r.cache = cache
	r.rootless = rootless
	r.proot = proot
	r.loggers = loggers
	return
}

func (b *BuildState) initBundle(cmd string) (err error) {
	entrypoint := []string{"/bin/sh", "-c"}
	if b.bundle == nil {
		var bb *bundle.BundleBuilder
		if b.image == nil {
			bb = bundle.Builder("")
		} else if bb, err = bundle.BuilderFromImage("", b.image); err != nil {
			return errors.Wrap(err, "image builder")
		}
		if b.rootless {
			bb.ToRootless()
		}
		if b.proot != "" {
			bb.SetPRootPath(b.proot)
		}
		bb.UseHostNetwork()
		bb.SetProcessEntrypoint(entrypoint)
		if cmd != "" {
			bb.SetProcessCmd([]string{cmd})
		}
		bundle, err := b.bundles.CreateBundle(bb, false)
		if err != nil {
			return errors.Wrap(err, "image builder")
		}
		b.bundle = bundle
	} else {
		if cmd != "" {
			spec, err := b.bundle.Spec()
			if err != nil {
				return err
			}
			specgen := generate.NewFromSpec(spec)
			specgen.SetProcessArgs(append(entrypoint, cmd))
			err = b.bundle.SetSpec(&specgen)
		}
	}
	return
}

func (b *BuildState) SetAuthor(author string) error {
	b.config.Author = author
	return b.cached("AUTHOR "+author, b.commitConfig)
}

func (b *BuildState) SetWorkingDir(dir string) error {
	dir = absFile(dir, b.config.Config.WorkingDir)
	b.config.Config.WorkingDir = dir
	return b.cached("WORKDIR "+dir, b.commitConfig)
}

// TODO: move into some shared package since this is a duplicate
func absFile(p, baseDir string) string {
	if filepath.IsAbs(p) {
		return p
	} else {
		return filepath.Join(baseDir, p)
	}
}

func (b *BuildState) SetEntrypoint(entrypoint []string) (err error) {
	entrypointJson, err := json.Marshal(entrypoint)
	if err != nil {
		return
	}
	b.config.Config.Entrypoint = entrypoint
	return b.cached("ENTRYPOINT "+string(entrypointJson), b.commitConfig)
}

func (b *BuildState) SetCmd(cmd []string) (err error) {
	cmdJson, err := json.Marshal(cmd)
	if err != nil {
		return
	}
	b.config.Config.Cmd = cmd
	return b.cached("CMD "+string(cmdJson), b.commitConfig)
}

func (b *BuildState) FromImage(image string) (err error) {
	if b.image != nil {
		return errors.New("base image must be defined as first build step")
	}
	img, e := b.images.ImageByName(image)
	// TODO: distinguish between 'image not found' and serious error
	if e != nil {
		if img, err = b.images.ImportImage(image); err != nil {
			return
		}
	}

	return b.setImage(&img)
}

func (b *BuildState) setImage(img *image.Image) (err error) {
	b.image = img
	b.config, err = img.Config()
	return
}

func (b *BuildState) Run(cmd string) (err error) {
	if b.image == nil {
		err = errors.New("cannot run a command in an empty image")
		return
	}

	comment := fmt.Sprintf("RUN /bin/sh -c %q", cmd)
	return b.cached(comment, func(comment string) (err error) {
		if err = b.initBundle(cmd); err != nil {
			return
		}

		// Run bundle and create new image layer from the result
		spec, err := b.bundle.Spec()
		if err != nil {
			return
		}
		rootfs := filepath.Join(b.bundle.Dir(), spec.Root.Path)
		manager, err := factory.NewContainerManager(rootfs, b.rootless, b.loggers)
		if err != nil {
			return
		}
		// TODO: move container creation into bundle init method and update the process here only
		container, err := manager.NewContainer(&run.ContainerConfig{
			Id:             b.bundle.ID(),
			Bundle:         b.bundle,
			Io:             run.NewStdContainerIO(),
			DestroyOnClose: true,
		})
		if err != nil {
			return
		}
		defer func() {
			if e := container.Close(); e != nil {
				err = multierror.Append(err, e)
			}
		}()

		if err = container.Start(); err != nil {
			return
		}
		if err = container.Wait(); err != nil {
			return
		}
		return b.commitLayer(comment)
	})
}

func (b *BuildState) Tag(tag string) (err error) {
	if b.image == nil {
		return errors.New("no image to tag provided")
	}
	img, err := b.images.TagImage(b.image.ID(), tag)
	if err == nil {
		b.image = &img
	}
	return
}

type FileEntry struct {
	Source      string
	Destination string
	// TODO: add mode
}

func (b *BuildState) CopyFile(contextDir string, patterns []string, dest, root string) (err error) {
	// TODO: build mtree diffs, merge them and let BlobStoreExt.diff create the layer without touching the bundle
	// => not possible with umoci's GenerateLayer/tarGenerator.AddFile methods
	defer func() {
		if err != nil {
			// Release bundle when operation failed
			if b.bundle != nil {
				err = multierror.Append(err, b.bundle.Close())
				b.bundle = nil
			}
			err = errors.Wrap(err, "copy file into image")
		}
	}()

	if err = b.initBundle(""); err != nil {
		return
	}
	// TODO: use empty temp directory if bundle does not already exist
	fs := NewFileSystemBuilder(filepath.Join(b.bundle.Dir(), "rootfs"), b.loggers.Debug)
	if err = fs.Add(contextDir, patterns, dest); err != nil {
		return
	}

	// TODO: Commit with exclusion rule for mtree
	// TODO: unique comment with hash sum from added files
	return b.commitLayer("add file to image")
}

func (b *BuildState) commitLayer(comment string) (err error) {
	defer func() {
		if err != nil {
			err = errors.Wrap(err, "commit layer")
		}
	}()

	b.loggers.Info.Println("  -> committing layer ...")

	rootfs := filepath.Join(b.bundle.Dir(), "rootfs")
	parentImageId := b.bundle.Image()
	img, err := b.images.AddImageLayer(rootfs, parentImageId, b.config.Author, comment)
	if err != nil {
		return
	}
	if err = b.setImage(&img); err != nil {
		return
	}
	newImageId := img.ID()
	return b.bundle.SetParentImageId(&newImageId)
}

func (b *BuildState) commitConfig(comment string) (err error) {
	defer func() {
		if err != nil {
			err = errors.Wrap(err, "commit config")
		}
	}()

	b.config.History = append(b.config.History, ispecs.History{
		CreatedBy:  b.config.Author,
		Comment:    comment,
		EmptyLayer: false,
	})
	var parentImgId *digest.Digest
	if b.image != nil {
		imgId := b.image.ID()
		parentImgId = &imgId
	}
	img, err := b.images.AddImageConfig(b.config, parentImgId)
	if err != nil {
		return
	}
	return b.setImage(&img)
}

func (b *BuildState) AddTag(name string) (err error) {
	img, err := b.images.TagImage(b.image.ID(), name)
	if err == nil {
		b.image = &img
	}
	return
}

func (b *BuildState) cached(uniqComment string, call func(comment string) error) (err error) {
	b.loggers.Info.Println(uniqComment)
	var parentImgId *digest.Digest
	if b.image != nil {
		pImgId := b.image.ID()
		parentImgId = &pImgId
	}
	var cachedImgId digest.Digest
	cachedImgId, err = b.cache.Get(parentImgId, uniqComment)
	if err == nil {
		var cachedImg image.Image
		if cachedImg, err = b.images.Image(cachedImgId); err == nil {
			// TODO: distinguish between image not found and serious error
			if err = b.setImage(&cachedImg); err != nil {
				return errors.Wrap(err, "cached image")
			}
			b.loggers.Info.Printf("  -> using cached image %s", cachedImg.ID())
			return
		}
	} else if e, ok := err.(CacheError); !ok || !e.Temporary() {
		// if no "entry not found" error
		return err
	} else {
		err = nil
	}

	b.loggers.Info.Println("  -> building ...")

	defer func() {
		if err != nil {
			// Release bundle when operation failed
			if b.bundle != nil {
				err = multierror.Append(err, b.bundle.Close())
				b.bundle = nil
			}
		}
	}()

	if err = call(uniqComment); err != nil {
		return
	}

	err = b.cache.Put(parentImgId, uniqComment, (*b.image).ID())

	b.loggers.Info.Printf("  -> built image %s", (*b.image).ID())

	return
}

func (b *BuildState) Close() (err error) {
	if b.bundle != nil {
		if e := b.bundle.Close(); e != nil {
			err = multierror.Append(err, e)
		}
	}
	return
}
