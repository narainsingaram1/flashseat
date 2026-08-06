import http from "k6/http";
import { check } from "k6";
import { expectedStatuses } from "k6/http";

export const BASE = __ENV.BASE_URL || "http://localhost:8080/api/v1";
http.setResponseCallback(expectedStatuses({ min: 200, max: 299 }, 409, 410));
export function session() { const r=http.post(`${BASE}/sessions/demo`); check(r,{"demo session created":x=>x.status===200}); return r.cookies.flashseat_session?.[0]?.value; }
export function headers(cookie,key){return{headers:{Cookie:`flashseat_session=${cookie}`,"Content-Type":"application/json","Idempotency-Key":key},tags:{name:"reservation_operation"}}}
export function availableSeats(cookie){const r=http.get(`${BASE}/events/aurora-2026/seats`,{headers:{Cookie:`flashseat_session=${cookie}`}});return r.json("seats").filter(s=>s.state==="available")}
export function hold(cookie,ids,key){return http.post(`${BASE}/holds`,JSON.stringify({eventId:"aurora-2026",seatIds:ids}),headers(cookie,key))}
