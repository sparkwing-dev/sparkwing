import type {Awaitable,Context,services as API} from "../../index.d.ts";
import {helper} from "./host.ts";
async function withServicesAddrs(ctx:Context,specs:readonly API.Service[],fn:(ctx:Context,addrs:readonly API.Addr[])=>Awaitable<void>):Promise<void>{
 const addrs=await helper<API.Addr[]>(ctx,"services.Start",[specs.map(({readyTimeoutMs,...spec})=>({...spec,readyTimeout:(readyTimeoutMs??0)*1000000}))]);
 try{await fn(ctx,addrs.map(({name,hostPort})=>({name,hostPort})))}finally{await helper(ctx,"services.Stop",[addrs],new AbortController().signal)}
}
export const services={defaultReadyTimeoutMs:30000,autoPort:-1,withServicesAddrs,withServices:(ctx:Context,specs:readonly API.Service[],fn:(ctx:Context)=>Awaitable<void>)=>withServicesAddrs(ctx,specs,fn)};
