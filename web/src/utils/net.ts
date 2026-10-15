/** "Public" when a socket listens on every interface. */
export function exposure(bind: string) {
  return bind === "0.0.0.0" || bind === "::" || bind === "*" ? "Public" : "Local only";
}
