/*
Copyright 2018 The Skaffold Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package build

import (
	"context"
	"io"
	"io/ioutil"
	"path/filepath"

	"github.com/GoogleContainerTools/kaniko/pkg/snapshot"
	kanikoutil "github.com/GoogleContainerTools/kaniko/pkg/util"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/color"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/constants"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/latest"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
)

// ArtifactCache is a map of hash to image name and tag
type ArtifactCache map[string]Artifact

type Cache struct {
	artifactCache ArtifactCache
	cacheFile     string
	useCache      bool
}

var (
	// For testing
	hashForArtifact = getHashForArtifact
)

// NewCache returns the current state of the cache
func NewCache(useCache bool, cacheFile string) *Cache {
	if !useCache {
		return &Cache{}
	}
	cf, err := resolveCacheFile(cacheFile)
	if err != nil {
		logrus.Warnf("Error resolving cache file, not using cache: %v", err)
		return &Cache{}
	}
	cache, err := retrieveArtifactCache(cf)
	if err != nil {
		logrus.Warnf("Error retrieving artifact cache, not using cache: %v", err)
		return &Cache{}
	}
	return &Cache{
		artifactCache: cache,
		cacheFile:     cf,
		useCache:      useCache,
	}
}

// resolveCacheFile makes sure that either a passed in cache file or the default cache file exists
func resolveCacheFile(cacheFile string) (string, error) {
	if cacheFile != "" {
		return cacheFile, util.VerifyOrCreateFile(cacheFile)
	}
	home, err := homedir.Dir()
	if err != nil {
		return "", errors.Wrap(err, "retrieving home directory")
	}
	defaultFile := filepath.Join(home, constants.DefaultSkaffoldDir, constants.DefaultCacheFile)
	return defaultFile, util.VerifyOrCreateFile(defaultFile)
}

func retrieveArtifactCache(cacheFile string) (ArtifactCache, error) {
	cache := ArtifactCache{}
	contents, err := ioutil.ReadFile(cacheFile)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(contents, &cache); err != nil {
		return nil, err
	}
	return cache, nil
}

// RetrieveCachedArtifacts checks to see if artifacts are cached, and returns tags for cached images otherwise a list of images to be built
func (c *Cache) RetrieveCachedArtifacts(ctx context.Context, out io.Writer, artifacts []*latest.Artifact) ([]*latest.Artifact, []Artifact, error) {
	if !c.useCache {
		return artifacts, nil, nil
	}
	var needToBuild []*latest.Artifact
	var built []Artifact
	for _, a := range artifacts {
		hash, err := hashForArtifact(ctx, a)
		if err != nil {
			logrus.Warnf("error getting hash for %s, skipping: %v", a.ImageName, err)
			needToBuild = append(needToBuild, a)
			continue
		}
		if val, ok := c.artifactCache[hash]; ok && val.ImageName == a.ImageName {
			color.Yellow.Fprintf(out, "Found %s in cache, skipping rebuild.\n", val.Tag)
			built = append(built, val)
			continue
		}
		needToBuild = append(needToBuild, a)
	}
	return needToBuild, built, nil
}

// CacheArtifacts determines the hash for each artifact, stores it in the artifact cache, and saves the cache at the end
func (c *Cache) CacheArtifacts(ctx context.Context, artifacts []*latest.Artifact, buildArtifacts []Artifact) error {
	if !c.useCache {
		return nil
	}
	tags := map[string]string{}
	for _, t := range buildArtifacts {
		tags[t.ImageName] = t.Tag
	}
	for _, a := range artifacts {
		hash, err := hashForArtifact(ctx, a)
		if err != nil {
			continue
		}
		c.artifactCache[hash] = Artifact{
			Tag:       tags[a.ImageName],
			ImageName: a.ImageName,
		}
	}
	return c.save()
}

// Save saves the artifactCache to the cacheFile
func (c *Cache) save() error {
	data, err := yaml.Marshal(c.artifactCache)
	if err != nil {
		return errors.Wrap(err, "marshalling hashes")
	}
	return ioutil.WriteFile(c.cacheFile, data, 0755)
}

func getHashForArtifact(ctx context.Context, a *latest.Artifact) (string, error) {
	deps, err := DependenciesForArtifact(ctx, a)
	if err != nil {
		return "", errors.Wrapf(err, "getting dependencies for %s", a.ImageName)
	}
	lm := snapshot.NewLayeredMap(kanikoutil.Hasher(), kanikoutil.CacheHasher())
	lm.Snapshot()
	for _, d := range deps {
		if err := lm.Add(d); err != nil {
			logrus.Warnf("Error adding %s: %v", d, err)
		}
	}
	return lm.Key()
}
