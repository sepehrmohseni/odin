# Demo backends for the development stack (users, products, orders, categories). Pick the
# service with the container command, e.g. `node users-service/server.js`.
FROM node:22.23.5-alpine

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund

COPY . .

USER node
EXPOSE 8081 8083 8084 8085
CMD ["node", "users-service/server.js"]
