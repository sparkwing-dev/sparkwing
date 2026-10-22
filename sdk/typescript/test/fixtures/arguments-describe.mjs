import { Plan, Generated, definePipeline } from "../../src/plan.ts";
import { describe } from "../../src/describe.ts";
const declarations = {
 args: [
  {name:"text", type:"string", required:true, enum:[" yes "," no "]},
  {name:"bool", type:"bool"}, {name:"int", type:"int"},
  {name:"int64", type:"int64"}, {name:"float", type:"float64"},
  {name:"duration", type:"duration"},
  {name:"list", type:"[]string", enum:[" dev "," prod "], default:"dev, prod"},
  {name:"empty", type:"string", default:""},
 ],
 "duplicate-name": [{name:"value",type:"string"},{name:"value",type:"string"}],
 "duplicate-short": [{name:"one",type:"string",short:"v"},{name:"two",type:"string",short:"v"}],
 "invalid-short": [{name:"one",type:"string",short:"long"}],
 "required-empty": [{name:"value",type:"string",required:true,default:""}],
 "required-default": [{name:"value",type:"string",required:true,default:"bad"}],
 "enum-type": [{name:"value",type:"int",enum:["1"],required:true}],
 "enum-anchor": [{name:"value",type:"string",enum:["one"]}],
 "enum-default": [{name:"value",type:"string",enum:["one"],default:"two"}],
 "float-default": [{name:"value",type:"float64",default:"NaN"}],
};
if (process.argv.includes("--conditional")) {
 const definition = definePipeline({name:"conditional",args:[{name:"enable",type:"bool"}],plan(p,run){
  p.job("source",()=>null);
  if (run.args.enable === "true") p.job("conditional",()=>null).withArgs({name:"token",type:"string",secret:true,default:"conditional-secret-default"});
 }});
 const run = {args:{},git:{},runner:{},id:"r",pipeline:"conditional"};
 const description = await describe(run);
 const plan = new Plan();
 await definition.plan(plan,{...run,args:{enable:"true"}});
 process.stdout.write(JSON.stringify({description,plan:plan.toDoc()}));
} else if (process.argv.includes("--generated")) {
 const jobs = {};
 for (const [name,args] of Object.entries(declarations)) {
  const plan = new Plan();
  const source = plan.job("source",()=>null);
  const generator = source.generate(()=>{});
  const generated = new Generated(source,generator.id,plan);
  generated.job("child",()=>null).withArgs(...args);
  jobs[name] = generated.toDoc().jobs;
 }
 process.stdout.write(JSON.stringify(jobs));
} else {
for (const [name,args] of Object.entries(declarations)) {
 definePipeline({name,args,plan(p){p.job("job",()=>null)}});
}
definePipeline({name:"secret-empty",secrets:[{name:""}],plan(p){p.job("job",()=>null)}});
definePipeline({name:"secret-duplicate",secrets:[{name:"TOKEN"},{name:"TOKEN"}],plan(p){p.job("job",()=>null)}});
process.stdout.write(JSON.stringify(await describe({args:{},git:{},runner:{},id:"r",pipeline:"args"})));

}
