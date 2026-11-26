// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algorithm

import "strings"

// awsSystemTagPrefix is the prefix for AWS-reserved system tags, which are immutable.
const awsSystemTagPrefix = "aws:"

// DiffStringMap will diff desired with current.
// Returns the k/v that need to be add/updated and k/v that need to be deleted to make current match desired.
func DiffStringMap(desired map[string]string, current map[string]string) (map[string]string, map[string]string) {
	modify := make(map[string]string)
	remove := make(map[string]string)

	for key, desiredVal := range desired {
		currentVal, ok := current[key]
		if !ok || currentVal != desiredVal {
			modify[key] = desiredVal
		}
	}

	for key, currentVal := range current {
		if _, ok := desired[key]; !ok {
			remove[key] = currentVal
		}
	}

	return modify, remove
}

// DiffStringMapIgnoreAWSTags is like DiffStringMap but filters out AWS-reserved system tags
// (prefixed with "aws:") from both results. These tags are immutable.
func DiffStringMapIgnoreAWSTags(desired map[string]string, current map[string]string) (map[string]string, map[string]string) {
	modify, remove := DiffStringMap(desired, current)
	RemoveKeysByPrefix(modify, awsSystemTagPrefix)
	RemoveKeysByPrefix(remove, awsSystemTagPrefix)
	return modify, remove
}

// RemoveKeysByPrefix removes all entries from the map whose key starts with the given prefix.
func RemoveKeysByPrefix(m map[string]string, prefix string) {
	for key := range m {
		if strings.HasPrefix(key, prefix) {
			delete(m, key)
		}
	}
}

// MergeStringMap merges the given string maps into a new map.
// Later maps override keys from earlier maps.
func MergeStringMap(maps ...map[string]string) map[string]string {
	merged := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			merged[k] = v
		}
	}
	return merged
}
