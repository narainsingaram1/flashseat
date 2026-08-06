import { session,availableSeats } from "../common.js";import { sleep } from "k6";
export const options={scenarios:{soak:{executor:"constant-vus",vus:Number(__ENV.VUS||100),duration:__ENV.DURATION||"30m"}},thresholds:{http_req_failed:["rate<0.01"],http_req_duration:["p(95)<100","p(99)<250"]}};export default function(){const c=session();availableSeats(c);sleep(1)}
