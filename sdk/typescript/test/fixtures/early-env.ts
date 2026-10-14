// A sibling module the entry imports before the SDK. ES modules evaluate in
// import order, so only the bootstrap preload scrubs the bearer before this
// copy is taken.
export const moduleEnv = { ...process.env };
