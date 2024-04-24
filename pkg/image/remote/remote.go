/*
Copyright 2018 Google LLC

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

package remote

import (
	"fmt"
	"strings"

	"github.com/GoogleContainerTools/kaniko/pkg/config"
	"github.com/GoogleContainerTools/kaniko/pkg/creds"
	"github.com/GoogleContainerTools/kaniko/pkg/util"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/sirupsen/logrus"
)

var (
	manifestCache   = make(map[string]v1.Image)
	remoteImageFunc = remote.Image
)

// RetrieveRemoteImage retrieves the manifest for the specified image from the specified registry
func RetrieveRemoteImage(image string, opts config.RegistryOptions, customPlatform string) (v1.Image, error) {
	logrus.Infof("Retrieving image manifest %s", image)

	cachedRemoteImage := manifestCache[image]
	if cachedRemoteImage != nil {
		logrus.Infof("Returning cached image manifest")
		return cachedRemoteImage, nil
	}

	ref, err := name.ParseReference(image, name.WeakValidation)
	if err != nil {
		return nil, err
	}

	if newRegURLs, found := opts.RegistryMaps[ref.Context().RegistryStr()]; found {
		for _, registryMapping := range newRegURLs {

			regToMapTo, repositoryPrefix := parseRegistryMapping(registryMapping)

			insecurePull := opts.InsecurePull || opts.InsecureRegistries.Contains(regToMapTo)

			rewrittenRepository, err := rewriteRepository(ref.Context(), regToMapTo, repositoryPrefix, insecurePull)
			if err != nil {
				return nil, err
			}

			rewrittenRef := setNewRepository(ref, rewrittenRepository)

			logrus.Infof("Retrieving image %s from mapped registry %s", rewrittenRef, regToMapTo)
			retryFunc := func() (v1.Image, error) {
				return remoteImageFunc(rewrittenRef, remoteOptions(regToMapTo, opts, customPlatform)...)
			}

			var remoteImage v1.Image
			if remoteImage, err = util.RetryWithResult(retryFunc, opts.ImageDownloadRetry, 1000); err != nil {
				logrus.Warnf("Failed to retrieve image %s from remapped registry %s: %s. Will try with the next registry, or fallback to the original registry.", rewrittenRef, regToMapTo, err)
				continue
			}

			manifestCache[image] = remoteImage

			return remoteImage, nil
		}

		if len(newRegURLs) > 0 && opts.SkipDefaultRegistryFallback {
			return nil, fmt.Errorf("image not found on any configured mapped registries for %s", ref)
		}
	}

	registryName := ref.Context().RegistryStr()
	if opts.InsecurePull || opts.InsecureRegistries.Contains(registryName) {
		newReg, err := name.NewRegistry(registryName, name.WeakValidation, name.Insecure)
		if err != nil {
			return nil, err
		}
		ref = setNewRegistry(ref, newReg)
	}

	logrus.Infof("Retrieving image %s from registry %s", ref, registryName)

	retryFunc := func() (v1.Image, error) {
		return remoteImageFunc(ref, remoteOptions(registryName, opts, customPlatform)...)
	}

	var remoteImage v1.Image
	if remoteImage, err = util.RetryWithResult(retryFunc, opts.ImageDownloadRetry, 1000); remoteImage != nil {
		manifestCache[image] = remoteImage
	}

	return remoteImage, err
}

// rewriteReference adds the library/ and/or the {path}/ in registry map suffix.
func rewriteRepository(repo name.Repository, regToMapTo string, repositoryPrefix string, insecurePull bool) (name.Repository, error) {
	if insecurePull {
		return name.NewRepository(repositoryPrefix+repo.RepositoryStr(), name.WithDefaultRegistry(regToMapTo), name.WeakValidation, name.Insecure)
	} else {
		return name.NewRepository(repositoryPrefix+repo.RepositoryStr(), name.WithDefaultRegistry(regToMapTo), name.WeakValidation)
	}
}

func setNewRepository(ref name.Reference, newRepo name.Repository) name.Reference {
	switch r := ref.(type) {
	case name.Tag:
		r.Repository = newRepo
		return r
	case name.Digest:
		r.Repository = newRepo
		return r
	default:
		return ref
	}
}

func setNewRegistry(ref name.Reference, newReg name.Registry) name.Reference {
	switch r := ref.(type) {
	case name.Tag:
		r.Repository.Registry = newReg
		return r
	case name.Digest:
		r.Repository.Registry = newReg
		return r
	default:
		return ref
	}
}

func remoteOptions(registryName string, opts config.RegistryOptions, customPlatform string) []remote.Option {
	tr, err := util.MakeTransport(opts, registryName)

	// The MakeTransport function will only return errors if there was a problem
	// with registry certificates (Verification or mTLS)
	if err != nil {
		logrus.Fatalf("Unable to setup transport for registry %q: %v", customPlatform, err)
	}

	// The platform value has previously been validated.
	platform, err := v1.ParsePlatform(customPlatform)
	if err != nil {
		logrus.Fatalf("Invalid platform %q: %v", customPlatform, err)
	}

	return []remote.Option{remote.WithTransport(tr), remote.WithAuthFromKeychain(creds.GetKeychain()), remote.WithPlatform(*platform)}
}

// Parse the registry mapping
// example: regMapping = "registry.example.com/namespace/repo" will return registry.example.com and namespace/repo/
func parseRegistryMapping(regMapping string) (string, string) {
	// Split the registry mapping by slashes
	segments := strings.Split(regMapping, "/")

	// Get the first segment to get the registry url
	regURL := segments[0]

	// Join all remaining segments
	repositoryPrefix := strings.Join(segments[1:], "/")

	if repositoryPrefix != "" {
		repositoryPrefix = repositoryPrefix + "/"
	}

	return regURL, repositoryPrefix
}
