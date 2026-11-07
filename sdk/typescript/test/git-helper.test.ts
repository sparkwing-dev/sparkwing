import assert from "node:assert/strict";
import test from "node:test";
import {git,Git,githubOwnerRepo} from "../src/index.ts";
import {registerHelpers} from "../src/helpers/host.ts";
import type {HostServices} from "../src/routes.ts";
import {fakeContext} from "./helper-context.ts";

test("Git helpers relay typed operations and retain immutable run facts",async()=>{
 const {ctx}=fakeContext(()=>{throw new Error("SDK spawned a command")});
 const calls:{operation:string;args:readonly unknown[]}[]=[];
 registerHelpers(ctx,{async helper(operation:string,args:readonly unknown[]){calls.push({operation,args});return operation==="git.CurrentSHA"?"abcdef":undefined}} as unknown as HostServices);
 assert.equal(await git.currentSHA(ctx,"/repo"),"abcdef");
 await git.clone(ctx,"remote","dest",{depth:2});
 await git.pushTag(ctx,"/repo","v0.1.0","release");
 assert.deepEqual(calls,[{operation:"git.CurrentSHA",args:["/repo"]},{operation:"git.Clone",args:["remote","dest",2]},{operation:"git.PushTag",args:["/repo","v0.1.0","release"]}]);
 const info=new Git("/repo",{repo:"owner/project",commit:"123456789012345",branch:"main"});
 assert.equal(info.shortSHA(),"123456789012");assert.equal(info.name(),"project");assert.deepEqual(githubOwnerRepo(" owner/project "),{owner:"owner",repo:"project"});
});
test("Git helpers require the bound host",async()=>{const {ctx}=fakeContext(()=>"");await assert.rejects(git.currentSHA(ctx,"/repo"),/host helper service unavailable/)});
