package gateio

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thrasher-corp/gocryptotrader/common"
	"github.com/thrasher-corp/gocryptotrader/common/convert"
	"github.com/thrasher-corp/gocryptotrader/config"
	"github.com/thrasher-corp/gocryptotrader/currency"
	exchange "github.com/thrasher-corp/gocryptotrader/exchanges"
	"github.com/thrasher-corp/gocryptotrader/exchanges/account"
	"github.com/thrasher-corp/gocryptotrader/exchanges/asset"
	"github.com/thrasher-corp/gocryptotrader/exchanges/order"
	"github.com/thrasher-corp/gocryptotrader/exchanges/orderbook"
	"github.com/thrasher-corp/gocryptotrader/exchanges/protocol"
	"github.com/thrasher-corp/gocryptotrader/exchanges/request"
	"github.com/thrasher-corp/gocryptotrader/exchanges/ticker"
	"github.com/thrasher-corp/gocryptotrader/exchanges/websocket/wshandler"
	"github.com/thrasher-corp/gocryptotrader/exchanges/withdraw"
	log "github.com/thrasher-corp/gocryptotrader/logger"
)

// GetDefaultConfig returns a default exchange config
func (g *Gateio) GetDefaultConfig() (*config.ExchangeConfig, error) {
	g.SetDefaults()
	exchCfg := new(config.ExchangeConfig)
	exchCfg.Name = g.Name
	exchCfg.HTTPTimeout = exchange.DefaultHTTPTimeout
	exchCfg.BaseCurrencies = g.BaseCurrencies

	err := g.SetupDefaults(exchCfg)
	if err != nil {
		return nil, err
	}

	if g.Features.Supports.RESTCapabilities.AutoPairUpdates {
		err = g.UpdateTradablePairs(true)
		if err != nil {
			return nil, err
		}
	}

	return exchCfg, nil
}

// SetDefaults sets default values for the exchange
func (g *Gateio) SetDefaults() {
	g.Name = "GateIO"
	g.Enabled = true
	g.Verbose = true
	g.API.CredentialsValidator.RequiresKey = true
	g.API.CredentialsValidator.RequiresSecret = true

	g.CurrencyPairs = currency.PairsManager{
		AssetTypes: asset.Items{
			asset.Spot,
		},
		UseGlobalFormat: true,
		RequestFormat: &currency.PairFormat{
			Delimiter: "_",
		},
		ConfigFormat: &currency.PairFormat{
			Delimiter: "_",
			Uppercase: true,
		},
	}

	g.Features = exchange.Features{
		Supports: exchange.FeaturesSupported{
			REST:      true,
			Websocket: true,
			RESTCapabilities: protocol.Features{
				TickerBatching:      true,
				TickerFetching:      true,
				KlineFetching:       true,
				TradeFetching:       true,
				OrderbookFetching:   true,
				AutoPairUpdates:     true,
				AccountInfo:         true,
				GetOrder:            true,
				GetOrders:           true,
				CancelOrders:        true,
				CancelOrder:         true,
				SubmitOrder:         true,
				UserTradeHistory:    true,
				CryptoDeposit:       true,
				CryptoWithdrawal:    true,
				TradeFee:            true,
				CryptoWithdrawalFee: true,
			},
			WebsocketCapabilities: protocol.Features{
				TickerFetching:         true,
				OrderbookFetching:      true,
				TradeFetching:          true,
				KlineFetching:          true,
				Subscribe:              true,
				Unsubscribe:            true,
				AuthenticatedEndpoints: true,
				MessageCorrelation:     true,
				GetOrder:               true,
				AccountBalance:         true,
			},
			WithdrawPermissions: exchange.AutoWithdrawCrypto |
				exchange.NoFiatWithdrawals,
		},
		Enabled: exchange.FeaturesEnabled{
			AutoPairUpdates: true,
		},
	}

	g.Requester = request.New(g.Name,
		request.NewRateLimit(time.Second*10, gateioAuthRate),
		request.NewRateLimit(time.Second*10, gateioUnauthRate),
		common.NewHTTPClientWithTimeout(exchange.DefaultHTTPTimeout))

	g.API.Endpoints.URLDefault = gateioTradeURL
	g.API.Endpoints.URL = g.API.Endpoints.URLDefault
	g.API.Endpoints.URLSecondaryDefault = gateioMarketURL
	g.API.Endpoints.URLSecondary = g.API.Endpoints.URLSecondaryDefault
	g.API.Endpoints.WebsocketURL = gateioWebsocketEndpoint
	g.Websocket = wshandler.New()
	g.WebsocketResponseMaxLimit = exchange.DefaultWebsocketResponseMaxLimit
	g.WebsocketResponseCheckTimeout = exchange.DefaultWebsocketResponseCheckTimeout
	g.WebsocketOrderbookBufferLimit = exchange.DefaultWebsocketOrderbookBufferLimit
}

// Setup sets user configuration
func (g *Gateio) Setup(exch *config.ExchangeConfig) error {
	if !exch.Enabled {
		g.SetEnabled(false)
		return nil
	}

	err := g.SetupDefaults(exch)
	if err != nil {
		return err
	}

	err = g.Websocket.Setup(
		&wshandler.WebsocketSetup{
			Enabled:                          exch.Features.Enabled.Websocket,
			Verbose:                          exch.Verbose,
			AuthenticatedWebsocketAPISupport: exch.API.AuthenticatedWebsocketSupport,
			WebsocketTimeout:                 exch.WebsocketTrafficTimeout,
			DefaultURL:                       gateioWebsocketEndpoint,
			ExchangeName:                     exch.Name,
			RunningURL:                       exch.API.Endpoints.WebsocketURL,
			Connector:                        g.WsConnect,
			Subscriber:                       g.Subscribe,
			UnSubscriber:                     g.Unsubscribe,
			Features:                         &g.Features.Supports.WebsocketCapabilities,
		})
	if err != nil {
		return err
	}

	g.WebsocketConn = &wshandler.WebsocketConnection{
		ExchangeName:         g.Name,
		URL:                  g.Websocket.GetWebsocketURL(),
		ProxyURL:             g.Websocket.GetProxyAddress(),
		Verbose:              g.Verbose,
		ResponseCheckTimeout: exch.WebsocketResponseCheckTimeout,
		ResponseMaxLimit:     exch.WebsocketResponseMaxLimit,
		RateLimit:            gateioWebsocketRateLimit,
	}

	g.Websocket.Orderbook.Setup(
		exch.WebsocketOrderbookBufferLimit,
		true,
		false,
		false,
		false,
		exch.Name)
	return nil
}

// Start starts the GateIO go routine
func (g *Gateio) Start(wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		g.Run()
		wg.Done()
	}()
}

// Run implements the GateIO wrapper
func (g *Gateio) Run() {
	if g.Verbose {
		g.PrintEnabledPairs()
	}

	if !g.GetEnabledFeatures().AutoPairUpdates {
		return
	}

	err := g.UpdateTradablePairs(false)
	if err != nil {
		log.Errorf(log.ExchangeSys, "%s failed to update tradable pairs. Err: %s", g.Name, err)
	}
}

// FetchTradablePairs returns a list of the exchanges tradable pairs
func (g *Gateio) FetchTradablePairs(asset asset.Item) ([]string, error) {
	return g.GetSymbols()
}

// UpdateTradablePairs updates the exchanges available pairs and stores
// them in the exchanges config
func (g *Gateio) UpdateTradablePairs(forceUpdate bool) error {
	pairs, err := g.FetchTradablePairs(asset.Spot)
	if err != nil {
		return err
	}

	return g.UpdatePairs(currency.NewPairsFromStrings(pairs), asset.Spot, false, forceUpdate)
}

// UpdateTicker updates and returns the ticker for a currency pair
func (g *Gateio) UpdateTicker(p currency.Pair, assetType asset.Item) (*ticker.Price, error) {
	tickerPrice := new(ticker.Price)
	result, err := g.GetTickers()
	if err != nil {
		return tickerPrice, err
	}
	pairs := g.GetEnabledPairs(assetType)
	for i := range pairs {
		for k := range result {
			if !strings.EqualFold(k, pairs[i].String()) {
				continue
			}
			tickerPrice = &ticker.Price{
				Last:        result[k].Last,
				High:        result[k].High,
				Low:         result[k].Low,
				Volume:      result[k].BaseVolume,
				QuoteVolume: result[k].QuoteVolume,
				Open:        result[k].Open,
				Close:       result[k].Close,
				Pair:        pairs[i],
			}
			err = ticker.ProcessTicker(g.Name, tickerPrice, assetType)
			if err != nil {
				log.Error(log.Ticker, err)
			}
		}
	}

	return ticker.GetTicker(g.Name, p, assetType)
}

// FetchTicker returns the ticker for a currency pair
func (g *Gateio) FetchTicker(p currency.Pair, assetType asset.Item) (*ticker.Price, error) {
	tickerNew, err := ticker.GetTicker(g.Name, p, assetType)
	if err != nil {
		return g.UpdateTicker(p, assetType)
	}
	return tickerNew, nil
}

// FetchOrderbook returns orderbook base on the currency pair
func (g *Gateio) FetchOrderbook(p currency.Pair, assetType asset.Item) (*orderbook.Base, error) {
	ob, err := orderbook.Get(g.Name, p, assetType)
	if err != nil {
		return g.UpdateOrderbook(p, assetType)
	}
	return ob, nil
}

// UpdateOrderbook updates and returns the orderbook for a currency pair
func (g *Gateio) UpdateOrderbook(p currency.Pair, assetType asset.Item) (*orderbook.Base, error) {
	orderBook := new(orderbook.Base)
	curr := g.FormatExchangeCurrency(p, assetType).String()

	orderbookNew, err := g.GetOrderbook(curr)
	if err != nil {
		return orderBook, err
	}

	for x := range orderbookNew.Bids {
		orderBook.Bids = append(orderBook.Bids, orderbook.Item{
			Amount: orderbookNew.Bids[x].Amount,
			Price:  orderbookNew.Bids[x].Price,
		})
	}

	for x := range orderbookNew.Asks {
		orderBook.Asks = append(orderBook.Asks, orderbook.Item{
			Amount: orderbookNew.Asks[x].Amount,
			Price:  orderbookNew.Asks[x].Price,
		})
	}

	orderBook.Pair = p
	orderBook.ExchangeName = g.Name
	orderBook.AssetType = assetType

	err = orderBook.Process()
	if err != nil {
		return orderBook, err
	}

	return orderbook.Get(g.Name, p, assetType)
}

// UpdateAccountInfo retrieves balances for all enabled currencies for the
// ZB exchange
func (g *Gateio) UpdateAccountInfo() (account.Holdings, error) {
	var info account.Holdings
	var balances []account.Balance

	if g.Websocket.CanUseAuthenticatedWebsocketForWrapper() {
		resp, err := g.wsGetBalance([]string{})
		if err != nil {
			return info, err
		}
		var currData []account.Balance
		for k := range resp.Result {
			currData = append(currData, account.Balance{
				CurrencyName: currency.NewCode(k),
				TotalValue:   resp.Result[k].Available + resp.Result[k].Freeze,
				Hold:         resp.Result[k].Freeze,
			})
		}
		info.Accounts = append(info.Accounts, account.SubAccount{
			Currencies: currData,
		})
	} else {
		balance, err := g.GetBalances()
		if err != nil {
			return info, err
		}

		switch l := balance.Locked.(type) {
		case map[string]interface{}:
			for x := range l {
				lockedF, err := strconv.ParseFloat(l[x].(string), 64)
				if err != nil {
					return info, err
				}

				balances = append(balances, account.Balance{
					CurrencyName: currency.NewCode(x),
					Hold:         lockedF,
				})
			}
		default:
			break
		}

		switch v := balance.Available.(type) {
		case map[string]interface{}:
			for x := range v {
				availAmount, err := strconv.ParseFloat(v[x].(string), 64)
				if err != nil {
					return info, err
				}

				var updated bool
				for i := range balances {
					if balances[i].CurrencyName == currency.NewCode(x) {
						balances[i].TotalValue = balances[i].Hold + availAmount
						updated = true
						break
					}
				}
				if !updated {
					balances = append(balances, account.Balance{
						CurrencyName: currency.NewCode(x),
						TotalValue:   availAmount,
					})
				}
			}
		default:
			break
		}

		info.Accounts = append(info.Accounts, account.SubAccount{
			Currencies: balances,
		})
	}

	info.Exchange = g.Name
	err := account.Process(&info)
	if err != nil {
		return account.Holdings{}, err
	}

	return info, nil
}

// FetchAccountInfo retrieves balances for all enabled currencies
func (g *Gateio) FetchAccountInfo() (account.Holdings, error) {
	acc, err := account.GetHoldings(g.Name)
	if err != nil {
		return g.UpdateAccountInfo()
	}

	return acc, nil
}

// GetFundingHistory returns funding history, deposits and
// withdrawals
func (g *Gateio) GetFundingHistory() ([]exchange.FundHistory, error) {
	return nil, common.ErrFunctionNotSupported
}

// GetExchangeHistory returns historic trade data since exchange opening.
func (g *Gateio) GetExchangeHistory(p currency.Pair, assetType asset.Item) ([]exchange.TradeHistory, error) {
	return nil, common.ErrNotYetImplemented
}

// SubmitOrder submits a new order
// TODO: support multiple order types (IOC)
func (g *Gateio) SubmitOrder(s *order.Submit) (order.SubmitResponse, error) {
	var submitOrderResponse order.SubmitResponse
	if err := s.Validate(); err != nil {
		return submitOrderResponse, err
	}

	var orderTypeFormat string
	if s.OrderSide == order.Buy {
		orderTypeFormat = order.Buy.Lower()
	} else {
		orderTypeFormat = order.Sell.Lower()
	}

	var spotNewOrderRequestParams = SpotNewOrderRequestParams{
		Amount: s.Amount,
		Price:  s.Price,
		Symbol: s.Pair.String(),
		Type:   orderTypeFormat,
	}

	response, err := g.SpotNewOrder(spotNewOrderRequestParams)
	if err != nil {
		return submitOrderResponse, err
	}
	if response.OrderNumber > 0 {
		submitOrderResponse.OrderID = strconv.FormatInt(response.OrderNumber, 10)
	}
	if response.LeftAmount == 0 {
		submitOrderResponse.FullyMatched = true
	}
	submitOrderResponse.IsOrderPlaced = true

	return submitOrderResponse, nil
}

// ModifyOrder will allow of changing orderbook placement and limit to
// market conversion
func (g *Gateio) ModifyOrder(action *order.Modify) (string, error) {
	return "", common.ErrFunctionNotSupported
}

// CancelOrder cancels an order by its corresponding ID number
func (g *Gateio) CancelOrder(order *order.Cancel) error {
	orderIDInt, err := strconv.ParseInt(order.OrderID, 10, 64)
	if err != nil {
		return err
	}
	_, err = g.CancelExistingOrder(orderIDInt,
		g.FormatExchangeCurrency(order.CurrencyPair, order.AssetType).String())
	return err
}

// CancelAllOrders cancels all orders associated with a currency pair
func (g *Gateio) CancelAllOrders(_ *order.Cancel) (order.CancelAllResponse, error) {
	cancelAllOrdersResponse := order.CancelAllResponse{
		Status: make(map[string]string),
	}
	openOrders, err := g.GetOpenOrders("")
	if err != nil {
		return cancelAllOrdersResponse, err
	}

	uniqueSymbols := make(map[string]int)
	for i := range openOrders.Orders {
		uniqueSymbols[openOrders.Orders[i].CurrencyPair]++
	}

	for unique := range uniqueSymbols {
		err = g.CancelAllExistingOrders(-1, unique)
		if err != nil {
			cancelAllOrdersResponse.Status[unique] = err.Error()
		}
	}

	return cancelAllOrdersResponse, nil
}

// GetOrderInfo returns information on a current open order
func (g *Gateio) GetOrderInfo(orderID string) (order.Detail, error) {
	var orderDetail order.Detail
	orders, err := g.GetOpenOrders("")
	if err != nil {
		return orderDetail, errors.New("failed to get open orders")
	}
	for x := range orders.Orders {
		if orders.Orders[x].OrderNumber != orderID {
			continue
		}
		orderDetail.Exchange = g.Name
		orderDetail.ID = orders.Orders[x].OrderNumber
		orderDetail.RemainingAmount = orders.Orders[x].InitialAmount - orders.Orders[x].FilledAmount
		orderDetail.ExecutedAmount = orders.Orders[x].FilledAmount
		orderDetail.Amount = orders.Orders[x].InitialAmount
		orderDetail.OrderDate = time.Unix(orders.Orders[x].Timestamp, 0)
		orderDetail.Status = order.Status(orders.Orders[x].Status)
		orderDetail.Price = orders.Orders[x].Rate
		orderDetail.CurrencyPair = currency.NewPairDelimiter(orders.Orders[x].CurrencyPair,
			g.GetPairFormat(asset.Spot, false).Delimiter)
		if strings.EqualFold(orders.Orders[x].Type, order.Ask.String()) {
			orderDetail.OrderSide = order.Ask
		} else if strings.EqualFold(orders.Orders[x].Type, order.Bid.String()) {
			orderDetail.OrderSide = order.Buy
		}
		return orderDetail, nil
	}
	return orderDetail, fmt.Errorf("no order found with id %v", orderID)
}

// GetDepositAddress returns a deposit address for a specified currency
func (g *Gateio) GetDepositAddress(cryptocurrency currency.Code, _ string) (string, error) {
	addr, err := g.GetCryptoDepositAddress(cryptocurrency.String())
	if err != nil {
		return "", err
	}

	if addr == gateioGenerateAddress {
		return "",
			errors.New("new deposit address is being generated, please retry again shortly")
	}
	return addr, nil
}

// WithdrawCryptocurrencyFunds returns a withdrawal ID when a withdrawal is
// submitted
func (g *Gateio) WithdrawCryptocurrencyFunds(withdrawRequest *withdraw.CryptoRequest) (string, error) {
	return g.WithdrawCrypto(withdrawRequest.Currency.String(), withdrawRequest.Address, withdrawRequest.Amount)
}

// WithdrawFiatFunds returns a withdrawal ID when a
// withdrawal is submitted
func (g *Gateio) WithdrawFiatFunds(withdrawRequest *withdraw.FiatRequest) (string, error) {
	return "", common.ErrFunctionNotSupported
}

// WithdrawFiatFundsToInternationalBank returns a withdrawal ID when a
// withdrawal is submitted
func (g *Gateio) WithdrawFiatFundsToInternationalBank(withdrawRequest *withdraw.FiatRequest) (string, error) {
	return "", common.ErrFunctionNotSupported
}

// GetWebsocket returns a pointer to the exchange websocket
func (g *Gateio) GetWebsocket() (*wshandler.Websocket, error) {
	return g.Websocket, nil
}

// GetFeeByType returns an estimate of fee based on type of transaction
func (g *Gateio) GetFeeByType(feeBuilder *exchange.FeeBuilder) (float64, error) {
	if !g.AllowAuthenticatedRequest() && // Todo check connection status
		feeBuilder.FeeType == exchange.CryptocurrencyTradeFee {
		feeBuilder.FeeType = exchange.OfflineTradeFee
	}
	return g.GetFee(feeBuilder)
}

// GetActiveOrders retrieves any orders that are active/open
func (g *Gateio) GetActiveOrders(req *order.GetOrdersRequest) ([]order.Detail, error) {
	var orders []order.Detail
	var currPair string
	if len(req.Currencies) == 1 {
		currPair = req.Currencies[0].String()
	}
	if g.Websocket.CanUseAuthenticatedWebsocketForWrapper() {
		for i := 0; ; i += 100 {
			resp, err := g.wsGetOrderInfo(req.OrderType.String(), i, 100)
			if err != nil {
				return orders, err
			}

			for j := range resp.WebSocketOrderQueryRecords {
				orderSide := order.Buy
				if resp.WebSocketOrderQueryRecords[j].Type == 1 {
					orderSide = order.Sell
				}
				orderType := order.Market
				if resp.WebSocketOrderQueryRecords[j].OrderType == 1 {
					orderType = order.Limit
				}
				firstNum, decNum, err := convert.SplitFloatDecimals(resp.WebSocketOrderQueryRecords[j].Ctime)
				if err != nil {
					return orders, err
				}
				orderDate := time.Unix(firstNum, decNum)
				orders = append(orders, order.Detail{
					Exchange:        g.Name,
					AccountID:       strconv.FormatInt(resp.WebSocketOrderQueryRecords[j].User, 10),
					ID:              strconv.FormatInt(resp.WebSocketOrderQueryRecords[j].ID, 10),
					CurrencyPair:    currency.NewPairFromString(resp.WebSocketOrderQueryRecords[j].Market),
					OrderSide:       orderSide,
					OrderType:       orderType,
					OrderDate:       orderDate,
					Price:           resp.WebSocketOrderQueryRecords[j].Price,
					Amount:          resp.WebSocketOrderQueryRecords[j].Amount,
					ExecutedAmount:  resp.WebSocketOrderQueryRecords[j].FilledAmount,
					RemainingAmount: resp.WebSocketOrderQueryRecords[j].Left,
					Fee:             resp.WebSocketOrderQueryRecords[j].DealFee,
				})
			}
			if len(resp.WebSocketOrderQueryRecords) < 100 {
				break
			}
		}
	} else {
		resp, err := g.GetOpenOrders(currPair)
		if err != nil {
			return nil, err
		}

		for i := range resp.Orders {
			if resp.Orders[i].Status != "open" {
				continue
			}

			symbol := currency.NewPairDelimiter(resp.Orders[i].CurrencyPair,
				g.GetPairFormat(asset.Spot, false).Delimiter)
			side := order.Side(strings.ToUpper(resp.Orders[i].Type))
			orderDate := time.Unix(resp.Orders[i].Timestamp, 0)
			orders = append(orders, order.Detail{
				ID:              resp.Orders[i].OrderNumber,
				Amount:          resp.Orders[i].Amount,
				Price:           resp.Orders[i].Rate,
				RemainingAmount: resp.Orders[i].FilledAmount,
				OrderDate:       orderDate,
				OrderSide:       side,
				Exchange:        g.Name,
				CurrencyPair:    symbol,
				Status:          order.Status(resp.Orders[i].Status),
			})
		}
	}
	order.FilterOrdersByTickRange(&orders, req.StartTicks, req.EndTicks)
	order.FilterOrdersBySide(&orders, req.OrderSide)
	return orders, nil
}

// GetOrderHistory retrieves account order information
// Can Limit response to specific order status
func (g *Gateio) GetOrderHistory(req *order.GetOrdersRequest) ([]order.Detail, error) {
	var trades []TradesResponse
	for i := range req.Currencies {
		resp, err := g.GetTradeHistory(req.Currencies[i].String())
		if err != nil {
			return nil, err
		}
		trades = append(trades, resp.Trades...)
	}

	var orders []order.Detail
	for _, trade := range trades {
		symbol := currency.NewPairDelimiter(trade.Pair,
			g.GetPairFormat(asset.Spot, false).Delimiter)
		side := order.Side(strings.ToUpper(trade.Type))
		orderDate := time.Unix(trade.TimeUnix, 0)
		orders = append(orders, order.Detail{
			ID:           strconv.FormatInt(trade.OrderID, 10),
			Amount:       trade.Amount,
			Price:        trade.Rate,
			OrderDate:    orderDate,
			OrderSide:    side,
			Exchange:     g.Name,
			CurrencyPair: symbol,
		})
	}

	order.FilterOrdersByTickRange(&orders, req.StartTicks, req.EndTicks)
	order.FilterOrdersBySide(&orders, req.OrderSide)
	return orders, nil
}

// SubscribeToWebsocketChannels appends to ChannelsToSubscribe
// which lets websocket.manageSubscriptions handle subscribing
func (g *Gateio) SubscribeToWebsocketChannels(channels []wshandler.WebsocketChannelSubscription) error {
	g.Websocket.SubscribeToChannels(channels)
	return nil
}

// UnsubscribeToWebsocketChannels removes from ChannelsToSubscribe
// which lets websocket.manageSubscriptions handle unsubscribing
func (g *Gateio) UnsubscribeToWebsocketChannels(channels []wshandler.WebsocketChannelSubscription) error {
	g.Websocket.RemoveSubscribedChannels(channels)
	return nil
}

// GetSubscriptions returns a copied list of subscriptions
func (g *Gateio) GetSubscriptions() ([]wshandler.WebsocketChannelSubscription, error) {
	return g.Websocket.GetSubscriptions(), nil
}

// AuthenticateWebsocket sends an authentication message to the websocket
func (g *Gateio) AuthenticateWebsocket() error {
	_, err := g.wsServerSignIn()
	return err
}

// ValidateCredentials validates current credentials used for wrapper
// functionality
func (g *Gateio) ValidateCredentials() error {
	_, err := g.UpdateAccountInfo()
	return g.CheckTransientError(err)
}
