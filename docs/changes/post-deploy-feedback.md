### Fixed

- Sustained post-deploy scan failures now create and update Bitwing tickets with run links, including when job details are unavailable. Production deploys flag the latest scan before rollout and report planning failures or scan jobs that failed before starting, while allowing repair deploys to continue.
