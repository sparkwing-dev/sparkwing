package protofd

// HandlesArg carries Windows request/frame handles as the next argv value,
// "<request>,<frame>" in decimal. Unix omits it and uses descriptors 3 and 4.
// SDK transport initialization clears inheritance before SDK tools can spawn.
const HandlesArg = "--sw-protocol-handles"
