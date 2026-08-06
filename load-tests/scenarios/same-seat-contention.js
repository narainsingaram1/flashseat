import { check } from "k6";
import exec from "k6/execution";
import { Counter } from "k6/metrics";
import { session, hold } from "../common.js";
const winners=new Counter("hold_winners");
export const options={scenarios:{collision:{executor:"per-vu-iterations",vus:Number(__ENV.VUS||1000),iterations:1,maxDuration:"90s"}},thresholds:{hold_winners:["count==1"],http_req_duration:["p(99)<250"]}};
export default function(){const c=session();const r=hold(c,[__ENV.SEAT_ID||"orchestra-2"],`collision-${exec.vu.idInTest}-${Date.now()}`);if(r.status===201)winners.add(1);check(r,{"exactly valid outcome":x=>x.status===201||x.status===409});}
