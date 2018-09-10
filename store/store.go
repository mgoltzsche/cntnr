package store

import (
	"path/filepath"

	"github.com/containers/image/types"
	"github.com/mgoltzsche/cntnr/bundle"
	bstore "github.com/mgoltzsche/cntnr/bundle/store"
	"github.com/mgoltzsche/cntnr/image"
	istore "github.com/mgoltzsche/cntnr/image/store"
	"github.com/mgoltzsche/cntnr/pkg/log"
	"github.com/pkg/errors"
)

// Minimal Store interface.
// containers/storage interface is not used to ease the OCI store implementation
// which is required by unprivileged users (https://github.com/containers/storage/issues/96)

var _ image.ImageStore = &Store{}

type Store struct {
	image.ImageStore
	bundle.BundleStore
}

type LockedStore struct {
	image.ImageStoreRW
	bundle.BundleStore
}

/*func (s *Store) ImageBuilderFromBundle(bundle bundle.Bundle, author string) (b *builder.ImageBuilder, err error) {
	rwstore, err := s.OpenLockedImageStore()
	if err != nil {
		return
	}
	if b, err = builder.NewImageBuilderFromBundle(rwstore, bundle, author); err != nil {
		err = errors.Wrap(err, "image builder from bundle")
		rwstore.Close()
	}
	return
}*/

func NewStore(dir string, rootless bool, systemContext *types.SystemContext, trustPolicy istore.TrustPolicyContext, loggers log.Loggers) (r Store, err error) {
	if dir == "" {
		return r, errors.New("init store: no store directory provided")
	}
	dir, err = filepath.Abs(dir)
	if err == nil {
		blobDir := filepath.Join(dir, "blobs")
		fsspecDir := filepath.Join(dir, ".fsspec")
		imageRepoDir := filepath.Join(dir, "image-repos")
		fsCacheDir := filepath.Join(dir, ".rofs-cache")
		imageIdDir := filepath.Join(dir, "image-ids")
		bundleDir := filepath.Join(dir, "bundles")
		tempDir := filepath.Join(dir, ".temp")
		mtreeStore := istore.NewFsSpecStore(fsspecDir, loggers.Debug)
		blobStore := istore.NewBlobStore(blobDir, loggers.Debug)
		blobStoreExt := istore.NewBlobStoreExt(&blobStore, &mtreeStore, rootless, loggers.Warn)
		rostore := istore.NewImageStoreRO(imageRepoDir, &blobStoreExt, istore.NewImageIdStore(imageIdDir), loggers.Warn)
		fsCache := istore.NewImageFSROCache(fsCacheDir)
		r.ImageStore, err = istore.NewImageStore(rostore, fsCache, tempDir, systemContext, trustPolicy, rootless, loggers)
		if err == nil {
			r.BundleStore, err = bstore.NewBundleStore(bundleDir, loggers.Info, loggers.Debug)
		}
	}
	return r, errors.Wrap(err, "init store")
}
