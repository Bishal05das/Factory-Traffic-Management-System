FROM node:22-alpine
WORKDIR /app
COPY controller-model.mjs controller-simulator.mjs ./
RUN mkdir /data && chown node:node /data
ENV SIMULATOR_STATE_FILE=/data/controller-state.json
USER node
CMD ["node", "controller-simulator.mjs"]
