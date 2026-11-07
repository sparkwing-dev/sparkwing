import * as paths from "node:path";
import type { Context, GitInfo, git as GitAPI } from "../../index.d.ts";
import { projectRoot } from "../scope.ts";
import { helper } from "./host.ts";
export const git={
clone:(ctx:Context,url: string, destDir: string, options: GitAPI.CloneOptions = {}):Promise<void>=>helper(ctx,"git.Clone",[url, destDir, options.depth ?? 0]),
fetch:(ctx:Context,directory: string):Promise<void>=>helper(ctx,"git.Fetch",[directory]),
shortCommit:(ctx:Context,directory: string):Promise<string>=>helper(ctx,"git.ShortCommit",[directory]),
currentSHA:(ctx:Context,directory: string):Promise<string>=>helper(ctx,"git.CurrentSHA",[directory]),
currentBranch:(ctx:Context,directory: string):Promise<string>=>helper(ctx,"git.CurrentBranch",[directory]),
defaultBranch:(ctx:Context,directory: string):Promise<string>=>helper(ctx,"git.DefaultBranch",[directory]),
remoteOriginURL:(ctx:Context,directory: string):Promise<string>=>helper(ctx,"git.RemoteOriginURL",[directory]),
isDirty:(ctx:Context,directory: string):Promise<boolean>=>helper(ctx,"git.IsDirty",[directory]),
filesetHash:(ctx:Context,directory: string):Promise<string>=>helper(ctx,"git.FilesetHash",[directory]),
changedFiles:(ctx:Context,directory: string, since: string):Promise<string[]>=>helper(ctx,"git.ChangedFiles",[directory,since]),
tagsAtHead:(ctx:Context,directory: string):Promise<string[]>=>helper(ctx,"git.TagsAtHead",[directory]),
latestTag:(ctx:Context,directory: string, prefix: string):Promise<string>=>helper(ctx,"git.LatestTag",[directory,prefix]),
tagExistsOnRemote:(ctx:Context,directory: string, tag: string):Promise<boolean>=>helper(ctx,"git.TagExistsOnRemote",[directory,tag]),
pushTag:(ctx:Context,directory: string, tag: string, message: string):Promise<void>=>helper(ctx,"git.PushTag",[directory,tag,message]),
};
export class Git implements GitInfo {
  readonly repo: string;
  readonly branch: string;
  readonly commit: string;
  readonly defaultBranch?: string;
  readonly repoURL?: string;
  #directory: string;
  constructor(directory: string, facts: GitInfo) {
    this.#directory = directory;
    this.repo = facts.repo;
    this.branch = facts.branch;
    this.commit = facts.commit;
    if (facts.defaultBranch !== undefined) this.defaultBranch = facts.defaultBranch;
    if (facts.repoURL !== undefined) this.repoURL = facts.repoURL;
    Object.freeze(this);
  }
  workDir(): string {
    return this.#directory;
  }
  shortSHA(): string {
    return this.commit.slice(0, 12);
  }
  name(): string {
    return this.repo.split("/").at(-1) ?? "";
  }
  isDirty(ctx: Context): Promise<boolean> {
    return git.isDirty(ctx, this.#directory);
  }
  filesetHash(ctx: Context): Promise<string> {
    return git.filesetHash(ctx, this.#directory);
  }
  changedFiles(ctx: Context, since: string): Promise<string[]> {
    return git.changedFiles(ctx, this.#directory, since);
  }
  tagsAtHead(ctx: Context): Promise<string[]> {
    return git.tagsAtHead(ctx, this.#directory);
  }
  latestTag(ctx: Context, prefix: string): Promise<string> {
    return git.latestTag(ctx, this.#directory, prefix);
  }
  pushTag(ctx: Context, tag: string, message: string): Promise<void> {
    return git.pushTag(ctx, this.#directory, tag, message);
  }
}

export async function gitFromTree(ctx: Context, directory: string): Promise<Git> {
  const root = paths.resolve(directory || projectRoot() || ".");
  const commit = await git.currentSHA(ctx, root);
  const branch = await git.currentBranch(ctx, root);
  const defaultBranch = await git.defaultBranch(ctx, root);
  const repoURL = await git.remoteOriginURL(ctx, root);
  const match = /^(?:git@github\.com:|https?:\/\/github\.com\/)([^/]+)\/([^/]+?)(?:\.git)?\/?$/.exec(repoURL);
  return new Git(root, { commit, branch, defaultBranch, repoURL, repo: match ? `${match[1]}/${match[2]}` : "" });
}

export function githubOwnerRepo(slug: string): { owner: string; repo: string } {
  const match = /^([^/]+)\/(.+)$/.exec(slug.trim());
  return match ? { owner: match[1]!, repo: match[2]! } : { owner: "", repo: "" };
}
