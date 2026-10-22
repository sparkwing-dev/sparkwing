import { makeContext } from "../../src/context.ts";
import { NodeRoutes } from "../../src/routes.ts";
const requests = [];
const services = new NodeRoutes({url:"http://127.0.0.1:12345",bearer:"fixture"},async (_url,options)=>{
 requests.push(JSON.parse(options.body));
 return new Response("{}");
});
const context = makeContext({run:{args:{},runner:{}},jobId:"job",args:{},signal:new AbortController().signal,frames:{write(){}},definition:{name:"fixture"},services,outputs:new Map()});
for (const annotation of [
 {message:"range",line:5,endLine:4},
 {message:"level",level:"warning"},
 {message:"negative",line:-1},
]) await context.annotate(annotation);
process.stdout.write(JSON.stringify(requests));
