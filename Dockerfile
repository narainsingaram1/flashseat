FROM node:24.5.0-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
FROM deps AS build
COPY . .
RUN npm run build
FROM node:24.5.0-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
RUN addgroup -S flashseat && adduser -S flashseat -G flashseat
COPY --from=build --chown=flashseat:flashseat /app ./
USER flashseat
EXPOSE 3000
CMD ["npm","start","--","--host","0.0.0.0"]
