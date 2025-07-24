#!/bin/bash

# OpenShift Synchronization Script
#
# This script synchronizes OpenShift-specific customizations from the upstream
# OpenShift fork back into the current working branch. It's used to maintain
# OpenShift-specific changes and configurations that differ from the upstream
# Kueue project.
#
# Prerequisites:
# - Must have an 'openshift' git remote configured pointing to the OpenShift fork
# - Current branch should be the target branch where you want to sync changes
#
# What this script does:
# 1. Syncs the OpenShift README from the main branch
# 2. Pulls OpenShift-specific build files (Makefiles, scripts) from release-0.11
# 3. Syncs dependency CRDs that are specific to OpenShift environments
# 4. Updates OpenShift-specific Kustomize configurations
# 5. Syncs OpenShift-specific package dependencies
#
# Usage: ./hack/openshift/sync.sh
#
# Note: Some sections are commented out as they may not be needed for every sync
# (e.g., Dockerfile.rhel, cherry-picking specific commits)

# Make sure you have a openshift remote

# get readme openshift template and update if there is anything missing in branch
git checkout openshift/main README_OPENSHIFT.md

# get Dockerfiles
# commented out because we don't need to sync this.
#git checkout openshift/release-0.11 Dockerfile.rhel

# get openshift make files
git checkout openshift/release-0.11 Makefile-test-ocp.mk
git checkout openshift/release-0.11 Makefile.ocp
git checkout openshift/release-0.11 hack/e2e-test-ocp.sh
git checkout openshift/release-0.11 hack/deploy-cert-manager-ocp.sh

# step get dep-crds folder
git checkout openshift/release-0.11 dep-crds

# step get ocp kustomize configs
git checkout openshift/release-0.11 config/default-ocp
git checkout openshift/release-0.11 config/components/manager-ocp

# step get ocp dependency magnet
git checkout openshift/release-0.11 pkg/openshift

# cherry pick openshift pods for pod, deployment and statefulsets.
# commented out as this branch already applied it
# git cherry-pick 577da0b2ece0fc16bc85d0a4feabe724deb443e4

