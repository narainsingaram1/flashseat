import exec from "k6/execution";import { check,sleep } from "k6";import { session,availableSeats,hold } from "../common.js";
export const options={vus:Number(__ENV.VUS||30),duration:__ENV.DURATION||"1m",thresholds:{http_req_duration:["p(99)<250"]}};
export default function(){const c=session(),s=availableSeats(c);if(!s.length){sleep(1);return}const r=hold(c,[s[exec.scenario.iterationInTest%s.length].id],`abandon-${exec.vu.idInTest}-${exec.scenario.iterationInTest}`);check(r,{"hold outcome valid":x=>x.status===201||x.status===409});sleep(1)}
