import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { ExecError, type HostExec } from "../src/command.ts";
const root=resolve(fileURLToPath(new URL("../../..",import.meta.url)));
let binary:string|undefined;
export const hostCommand:HostExec=async(request,signal)=>{
 // safety: the helper must compile against the published SDK pin, as root release builds do.
 if(!binary){binary=join(mkdtempSync(join(tmpdir(),"sdk-command-host-")),"host");execFileSync("go",["-C",root,"build","-p","1","-o",binary,"./sdk/typescript/test/testdata/commandhost"],{env:{...process.env,GOWORK:"off",GOMAXPROCS:"2"}})}
 return commandHost(binary!)(request,signal);
};
export function commandHost(program:string,args:string[]=[]):HostExec {
 return async(request,signal)=>await new Promise((resolveResult,reject)=>{
 const child=spawn(program,args,{stdio:["pipe","pipe","pipe"]});
 let buffer="";const stdout:Buffer[]=[],stderr:Buffer[]=[],hostErrors:Buffer[]=[];let final:any;
 const abort=()=>child.kill("SIGTERM");signal.addEventListener("abort",abort,{once:true});
 child.on("error",reject);child.stderr.on("data",data=>{hostErrors.push(Buffer.from(data));process.stderr.write(data)});
 child.stdout.on("data",chunk=>{buffer+=String(chunk);for(;;){const index=buffer.indexOf("\n");if(index<0)break;const data=JSON.parse(buffer.slice(0,index));buffer=buffer.slice(index+1);if(data.exec_output){const bytes=Buffer.from(data.data,"base64");(data.stream==="stdout"?stdout:stderr).push(bytes);request.onOutput?.(data.stream,bytes)}else final=data}});
 child.on("close",code=>{
  signal.removeEventListener("abort",abort);
  if(!final){reject(new Error(`command host exited ${code}`));return}
  const result={exitCode:final.exit_code,signaled:!!final.signaled,notFound:!!final.not_found,...(final.start_error?{startError:final.start_error}:{}),stdout:request.capture?Buffer.concat(stdout):Buffer.concat(stdout).subarray(-65536),stderr:request.capture?Buffer.concat(stderr):Buffer.concat(stderr).subarray(-65536)};
  if(code!==0){
   const cause=new Error(Buffer.concat(hostErrors).toString().trim()||`command host exited ${code}`);
   const failure=new ExecError({command:request.argv.join(" "),stdout:result.stdout.toString(),stderr:result.stderr.toString(),exitCode:result.exitCode},result.signaled?"signal":"",cause);
   failure.message+=`\ncommand host cleanup failed: ${cause.message}`;
   reject(failure);return;
  }
  resolveResult(result);
 });
 child.stdin.end(JSON.stringify({exec:"test",...request,...(request.stdin===undefined?{}:{stdin:Buffer.from(request.stdin).toString("base64")})})+"\n");
 if(signal.aborted)abort();
 });
}
