# lightning onion routing

My lame attempt at trying to understand the Lightning Network's onion routing.

It roughly follows [BOLT#4](https://github.com/lightning/bolts/blob/master/04-onion-routing.md) but focused mostly on how to 
construct and decrypt the onion. 


It does the following route:
```
Alice (origin node) -> Bob -> Charlie -> Dave (final node)
```

### build

```
go build -o lnonion main.go
```

### Build the onion
```
./lnonion onion
```
This will start a prompt to specify a payload for each hop (i.e bob, charlie, dave).

Something like this:
```
start building the onion. What payload do you want to put for Bob:
hi bob
What payload do you want to put for Charlie (2nd hop):
hi charlie
What payload do you want to put for Dave (last hop):
hi dave
onion to pass to first hop (bob): <onion>
```

After specifying the payload, it will return an onion that can be sent to the first hop in the route (bob).


### Peel the onion

To start peeling the onion, pass it to the first hop.
```
./lnonion parse --hop "bob" "<onion here>"
```
This will print the payload that was intended for this hop (bob) and then the onion to pass to the next hop (charlie).

Continue peeling the onion until it gets to the final hop (dave).
```
./lnonion parse --hop "charlie" "<onion from previous parse>"
```


#### credits
[BOLT#4](https://github.com/lightning/bolts/blob/master/04-onion-routing.md)

[Lightning Network Onion Routing: Sphinx Packet Construction](https://ellemouton.com/posts/sphinx/)

[lightning-onion](https://github.com/lightningnetwork/lightning-onion)

[onion](https://github.com/ellemouton/onion)
