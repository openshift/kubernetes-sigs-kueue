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

