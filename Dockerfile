FROM node:24.5.0-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM deps AS build
ARG NEXT_PUBLIC_API_URL=http://localhost:8080/api/v1
ENV NEXT_PUBLIC_API_URL=$NEXT_PUBLIC_API_URL NEXT_TELEMETRY_DISABLED=1
COPY . .
RUN npm run build

FROM node:24.5.0-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
RUN addgroup -S flashseat && adduser -S flashseat -G flashseat
COPY --from=build --chown=flashseat:flashseat /app/.next/standalone ./
COPY --from=build --chown=flashseat:flashseat /app/.next/static ./.next/static
COPY --from=build --chown=flashseat:flashseat /app/public ./public
USER flashseat
EXPOSE 3000
CMD ["node", "server.js"]
