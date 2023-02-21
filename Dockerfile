FROM scratch
COPY rewinged /bin/rewinged
ENTRYPOINT ["/bin/rewinged"]
